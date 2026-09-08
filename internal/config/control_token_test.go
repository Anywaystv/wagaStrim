// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package config

import (
	"encoding/hex"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestControlTokenCreatedOnceAndSaved(t *testing.T) {
	cfg := &Config{path: t.TempDir() + "/config.json"}
	token, err := cfg.CreateControlToken()
	require.NoError(t, err)
	raw, err := hex.DecodeString(token)
	require.NoError(t, err)
	assert.Len(t, raw, 32)
	assert.Equal(t, token, cfg.Token())
	saved, err := os.ReadFile(cfg.Path())
	require.NoError(t, err)
	assert.Contains(t, string(saved), token)
	_, err = cfg.CreateControlToken()
	require.ErrorIs(t, err, ErrTokenExists)
	assert.Equal(t, token, cfg.Token())
}

func TestProvisioningSaveFailureRollsBack(t *testing.T) {
	cfg := &Config{path: t.TempDir()}
	_, err := cfg.CreateControlToken()
	require.ErrorIs(t, err, ErrWriteConfig)
	assert.Empty(t, cfg.Token())
	_, err = cfg.AddIngest("Not saved")
	require.ErrorIs(t, err, ErrWriteConfig)
	assert.Empty(t, cfg.List())
	cfg.Ingests = []Ingest{{ID: "keep"}}
	_, err = cfg.ReplaceIngests(nil)
	require.ErrorIs(t, err, ErrWriteConfig)
	assert.Equal(t, "keep", cfg.List()[0].ID)
}
