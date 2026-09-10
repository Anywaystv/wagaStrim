// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDelayIsClampedToTheFloor(t *testing.T) {
	cfg := &Config{Ingests: []Ingest{{DelayMS: -10}, {DelayMS: 500}, {DelayMS: 99999}}}
	cfg.normalise()

	assert.Equal(t, 0, cfg.Ingests[0].DelayMS, "negative values clamp to zero")
	assert.Equal(t, 500, cfg.Ingests[1].DelayMS, "valid delays are preserved")
	assert.Equal(t, DelayMaxMS, cfg.Ingests[2].DelayMS, "above max must fall to the max")
}

func TestNormaliseFillsPortsAndCodecs(t *testing.T) {
	cfg := &Config{Ingests: []Ingest{{DelayMS: DelayDefaultMS}}}
	cfg.normalise()

	assert.Equal(t, DefaultMediaPort, cfg.MediaPort)
	assert.Equal(t, DefaultSignalPort, cfg.SignalPort)
	assert.Equal(t, DefaultUIPort, cfg.UIPort)
	assert.Equal(t, []string{CodecH264}, cfg.Ingests[0].Codecs)
}

func TestKeysAreRolePrefixedAndDistinct(t *testing.T) {
	cfg := &Config{path: t.TempDir() + "/config.json"}

	ing, err := cfg.AddIngest("Chest cam")
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(ing.SenderKey, SenderPrefix), "sender key needs its role prefix")
	assert.True(t, strings.HasPrefix(ing.ReceiverKey, ReceiverPrefix), "receiver key needs its role prefix")
	assert.NotEqual(t, ing.SenderKey, ing.ReceiverKey, "a shared key would let a viewer publish")
	assert.Len(t, ing.SenderKey, len(SenderPrefix)+keyBytes*2)
	assert.Equal(t, DelayDefaultMS, ing.DelayMS)
}

func TestRemoveRevokesAndRejectsUnknown(t *testing.T) {
	cfg := &Config{path: t.TempDir() + "/config.json"}

	ing, err := cfg.AddIngest("Drone")
	require.NoError(t, err)
	require.NoError(t, cfg.RemoveIngest(ing.ID))
	assert.Empty(t, cfg.Ingests)

	assert.ErrorIs(t, cfg.RemoveIngest(ing.ID), ErrUnknownIngest)
	assert.ErrorIs(t, cfg.SetDelay("nope", 3000), ErrUnknownIngest)
}

func TestSetDelayClamps(t *testing.T) {
	cfg := &Config{path: t.TempDir() + "/config.json"}

	ing, err := cfg.AddIngest("Handheld")
	require.NoError(t, err)
	assert.Equal(t, 2000, ing.DelayMS, "new cameras start with two seconds")
	require.NoError(t, cfg.SetDelay(ing.ID, 0))
	assert.Equal(t, 0, cfg.Ingests[0].DelayMS, "zero delay is supported")
	require.NoError(t, cfg.SetDelay(ing.ID, -10))

	assert.Equal(t, DelayFloorMS, cfg.Ingests[0].DelayMS, "the floor is not a suggestion")
}

func TestSaveIsOwnerOnly(t *testing.T) {
	cfg := &Config{path: t.TempDir() + "/config.json"}
	require.NoError(t, cfg.Save())

	info, err := os.Stat(cfg.path)
	require.NoError(t, err)
	assert.Equal(t, "-rw-------", info.Mode().String(), "keys live in this file")
}

func TestFailedSettingsSaveRollsBack(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Config) error
	}{
		{"delay", func(c *Config) error { return c.SetDelay("middle", 3000) }},
		{"group", func(c *Config) error { return c.SetSyncGroup("middle", "rig") }},
		{"label", func(c *Config) error { return c.SetLabel("middle", "renamed") }},
		{"codecs", func(c *Config) error { return c.SetCodecs("middle", []string{CodecH265}) }},
		{"remove first", func(c *Config) error { return c.RemoveIngest("first") }},
		{"remove middle", func(c *Config) error { return c.RemoveIngest("middle") }},
		{"remove last", func(c *Config) error { return c.RemoveIngest("last") }},
		{"host", func(c *Config) error { return c.SetPublicHost("192.0.2.1") }},
		{"autostart", func(c *Config) error { return c.SetAutostart(true) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{path: t.TempDir() + "/config.json", Ingests: []Ingest{
				pushed("first", 'a'), pushed("middle", 'c'), pushed("last", 'e'),
			}}
			require.NoError(t, cfg.Save())
			before, err := json.Marshal(cfg)
			require.NoError(t, err)
			path := cfg.path
			// A file cannot be renamed over a directory, even when run as root.
			cfg.path = t.TempDir()
			require.ErrorIs(t, tc.change(cfg), ErrWriteConfig)
			cfg.path = path
			after, err := json.Marshal(cfg)
			require.NoError(t, err)
			assert.JSONEq(t, string(before), string(after), "failed saves must not change memory")
			saved, err := os.ReadFile(cfg.Path())
			require.NoError(t, err)
			assert.JSONEq(t, string(before), string(saved))

			require.NoError(t, tc.change(cfg), "retry after storage recovers")
			after, err = json.Marshal(cfg)
			require.NoError(t, err)
			assert.NotEqual(t, string(before), string(after))
			saved, err = os.ReadFile(cfg.Path())
			require.NoError(t, err)
			assert.JSONEq(t, string(after), string(saved))
		})
	}
}

func TestSaveDoesNotReusePredictableTemporaryFile(t *testing.T) {
	cfg := &Config{path: t.TempDir() + "/config.json"}
	require.NoError(t, os.WriteFile(cfg.path+".tmp", []byte("untouched"), 0o600))
	require.NoError(t, cfg.Save())
	stale, err := os.ReadFile(cfg.path + ".tmp")
	require.NoError(t, err)
	assert.Equal(t, "untouched", string(stale))
}

// The mime type comes from pion and the identifier is ours, so the mapping is
// the one place a codec rename would silently stop reporting.
func TestANegotiatedMimeTypeRendersAsALabel(t *testing.T) {
	assert.Equal(t, "H.264", CodecLabelOf("video/H264"))
	assert.Equal(t, "H.265", CodecLabelOf("video/H265"))
	assert.Equal(t, "AV1", CodecLabelOf("video/AV1"))

	assert.Empty(t, CodecLabelOf("video/VP8"), "a codec the relay never registered has no label")
}
