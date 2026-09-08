// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
)

var ErrTokenExists = errors.New("control token is already configured")

func (c *Config) Token() string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.ControlToken
}

// CreateControlToken only bootstraps access; it never replaces an existing credential.
// The control listener starts on the next application launch.
func (c *Config) CreateControlToken() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ControlToken != "" {
		return "", ErrTokenExists
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", fmt.Errorf("%w: %w", ErrGenerateKey, err)
	}
	c.ControlToken = hex.EncodeToString(secret[:])
	if err := c.saveLocked(); err != nil {
		c.ControlToken = ""

		return "", err
	}

	return c.ControlToken, nil
}
