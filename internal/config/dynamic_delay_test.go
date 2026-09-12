// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package config

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/Anywaystv/wagaStrim/internal/dynamicdelay"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDynamicDelayPersistsBoundsAndPreservesFixedGroups(t *testing.T) {
	cfg := &Config{path: t.TempDir() + "/config.json"}
	camera, err := cfg.AddIngest("Camera")
	require.NoError(t, err)
	assert.False(t, camera.Enabled)
	assert.Equal(t, 10000, camera.MaximumMS)
	assert.Equal(t, 10, camera.CatchUpMSPerSecond)
	options := dynamicdelay.Options{Enabled: true, MaximumMS: 2500, Jump: false, CatchUpMSPerSecond: 50}
	require.NoError(t, cfg.SetDynamicDelay(camera.ID, options))
	require.NoError(t, cfg.SetDelay(camera.ID, 3000))
	assert.Equal(t, 3000, cfg.DelayOptions(camera.ID).MaximumMS)
	assert.False(t, cfg.DelayOptions(camera.ID).Jump)
	require.NoError(t, cfg.SetSyncGroup(camera.ID, "rig"))
	assert.False(t, cfg.DelayOptions(camera.ID).Enabled)
	require.NoError(t, cfg.SetSyncGroup(camera.ID, ""))
	assert.True(t, cfg.DelayOptions(camera.ID).Enabled)
	assert.Equal(t, 50, cfg.DelayOptions(camera.ID).CatchUpMSPerSecond)
	raw, err := os.ReadFile(cfg.path)
	require.NoError(t, err)
	var saved Config
	require.NoError(t, json.Unmarshal(raw, &saved))
	assert.Equal(t, cfg.List(), saved.List())
	before := cfg.List()
	cfg.path = t.TempDir()
	assert.Error(t, cfg.SetDynamicDelay(camera.ID, dynamicdelay.Options{}))
	assert.Equal(t, before, cfg.List())
}
