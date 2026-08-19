// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
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

// validKey reports whether a key has the shape newKey produces. A deployment
// that owns the ingest list supplies its own keys, and a key that is short, or
// upper case, or missing its prefix is a link that will never resolve, so it is
// rejected where it arrives rather than at the first failed publish.
func validKey(prefix, key string) bool {
	if len(key) != len(prefix)+hex.EncodedLen(keyBytes) {
		return false
	}

	if !strings.HasPrefix(key, prefix) {
		return false
	}

	_, err := hex.DecodeString(strings.ToLower(key[len(prefix):]))

	return err == nil && key == strings.ToLower(key)
}

// NewResourceKey returns an opaque identifier for a live session. It is not a
// credential: it names a resource the publisher already authenticated to create.
func NewResourceKey() (string, error) {
	return newKey("")
}
