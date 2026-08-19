// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// Key role prefixes. They make a link self-identifying once unmasked, so a link
// pasted out of context still says which application it belongs in.
const (
	SenderPrefix   = "s_"
	ReceiverPrefix = "r_"
)

const keyBytes = 16

// newKey returns a role-prefixed key of 32 hexadecimal characters.
func newKey(prefix string) (string, error) {
	buf := make([]byte, keyBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("%w: %w", ErrGenerateKey, err)
	}

	return prefix + hex.EncodeToString(buf), nil
}
