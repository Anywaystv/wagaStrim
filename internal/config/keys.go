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

// NewResourceKey returns an opaque identifier for a live session. It is not a
// credential: it names a resource the publisher already authenticated to create.
func NewResourceKey() (string, error) {
	return newKey("")
}

// NewTestIngest builds an ingest with real keys without touching the disk.
func NewTestIngest(label string) (*Ingest, error) {
	senderKey, err := newKey(SenderPrefix)
	if err != nil {
		return nil, err
	}

	receiverKey, err := newKey(ReceiverPrefix)
	if err != nil {
		return nil, err
	}

	id, err := newKey("")
	if err != nil {
		return nil, err
	}

	return &Ingest{
		ID:          id,
		Label:       label,
		SenderKey:   senderKey,
		ReceiverKey: receiverKey,
		Codecs:      []string{CodecH264},
		DelayMS:     DelayDefaultMS,
	}, nil
}
