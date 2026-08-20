// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package config

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDelayIsClampedToTheFloor(t *testing.T) {
	cfg := &Config{Ingests: []Ingest{{DelayMS: 0}, {DelayMS: 500}, {DelayMS: 99999}}}
	cfg.normalise()

	assert.Equal(t, DelayFloorMS, cfg.Ingests[0].DelayMS, "zero must rise to the floor")
	assert.Equal(t, DelayFloorMS, cfg.Ingests[1].DelayMS, "below floor must rise to the floor")
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
	require.NoError(t, cfg.SetDelay(ing.ID, 10))

	assert.Equal(t, DelayFloorMS, cfg.Ingests[0].DelayMS, "the floor is not a suggestion")
}

func TestSaveIsOwnerOnly(t *testing.T) {
	cfg := &Config{path: t.TempDir() + "/config.json"}
	require.NoError(t, cfg.Save())

	info, err := os.Stat(cfg.path)
	require.NoError(t, err)
	assert.Equal(t, "-rw-------", info.Mode().String(), "keys live in this file")
}

// The mime type comes from pion and the identifier is ours, so the mapping is
// the one place a codec rename would silently stop reporting.
func TestANegotiatedMimeTypeRendersAsALabel(t *testing.T) {
	assert.Equal(t, "H.264", CodecLabelOf("video/H264"))
	assert.Equal(t, "H.265", CodecLabelOf("video/H265"))
	assert.Equal(t, "AV1", CodecLabelOf("video/AV1"))

	assert.Empty(t, CodecLabelOf("video/VP8"), "a codec the relay never registered has no label")
}
