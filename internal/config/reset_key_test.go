// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package config

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResetSenderKeyPreservesCameraAndViewer(t *testing.T) {
	cfg := &Config{path: t.TempDir() + "/config.json"}
	before, err := cfg.AddIngest("Phone")
	require.NoError(t, err)
	after, err := cfg.ResetSenderKey(before.ID)
	require.NoError(t, err)
	assert.NotEqual(t, before.SenderKey, after.SenderKey)
	assert.True(t, validKey(SenderPrefix, after.SenderKey))
	_, role := cfg.Resolve(before.SenderKey)
	assert.Equal(t, RoleNone, role)
	_, role = cfg.Resolve(after.SenderKey)
	assert.Equal(t, RoleSender, role)
	_, role = cfg.Resolve(before.ReceiverKey)
	assert.Equal(t, RoleReceiver, role)
	before.SenderKey = after.SenderKey
	assert.Equal(t, before, after)
	raw, err := os.ReadFile(cfg.Path())
	require.NoError(t, err)
	var saved Config
	require.NoError(t, json.Unmarshal(raw, &saved))
	assert.Equal(t, after, saved.Ingests[0])
	_, err = cfg.ResetSenderKey("missing")
	require.ErrorIs(t, err, ErrUnknownIngest)
}

func TestResetSenderKeyRollsBackFailedSave(t *testing.T) {
	cfg := &Config{path: t.TempDir() + "/config.json"}
	before, err := cfg.AddIngest("Phone")
	require.NoError(t, err)
	cfg.path = t.TempDir()
	_, err = cfg.ResetSenderKey(before.ID)
	require.ErrorIs(t, err, ErrWriteConfig)
	assert.Equal(t, before, cfg.List()[0])
	_, role := cfg.Resolve(before.SenderKey)
	assert.Equal(t, RoleSender, role)
}
