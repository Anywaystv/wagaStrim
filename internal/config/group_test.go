// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func threeCams(t *testing.T) (*Config, [3]string) {
	t.Helper()

	cfg := &Config{path: t.TempDir() + "/config.json"}

	var ids [3]string

	for idx, name := range []string{"chest", "drone", "handheld"} {
		ing, err := cfg.AddIngest(name)
		require.NoError(t, err)

		ids[idx] = ing.ID
	}

	return cfg, ids
}

func TestUngroupedCameraKeepsItsOwnDelay(t *testing.T) {
	cfg, ids := threeCams(t)
	require.NoError(t, cfg.SetDelay(ids[0], 3000))

	assert.Equal(t, 3000, cfg.EffectiveDelay(ids[0]))
	assert.Equal(t, DelayDefaultMS, cfg.EffectiveDelay(ids[1]),
		"an ungrouped camera must not be slowed by a neighbor")
}

// Two cameras on screen together drift unless they share a target, and the
// shared target has to be the slowest member or the slow one still lags.
func TestGroupTakesTheSlowestMember(t *testing.T) {
	cfg, ids := threeCams(t)

	require.NoError(t, cfg.SetSyncGroup(ids[0], "stage"))
	require.NoError(t, cfg.SetSyncGroup(ids[1], "stage"))
	require.NoError(t, cfg.SetDelay(ids[1], 6000))

	assert.Equal(t, 6000, cfg.EffectiveDelay(ids[0]), "the fast camera waits for the slow one")
	assert.Equal(t, 6000, cfg.EffectiveDelay(ids[1]))
	assert.Equal(t, DelayDefaultMS, cfg.EffectiveDelay(ids[2]),
		"a camera outside the group is unaffected")
}

func TestLeavingAGroupReleasesTheRest(t *testing.T) {
	cfg, ids := threeCams(t)

	require.NoError(t, cfg.SetSyncGroup(ids[0], "stage"))
	require.NoError(t, cfg.SetSyncGroup(ids[1], "stage"))
	require.NoError(t, cfg.SetDelay(ids[1], 6000))
	require.Equal(t, 6000, cfg.EffectiveDelay(ids[0]))

	require.NoError(t, cfg.SetSyncGroup(ids[1], ""))

	assert.Equal(t, DelayDefaultMS, cfg.EffectiveDelay(ids[0]),
		"losing the slowest member must let the rest speed back up")
}

func TestGroupPeersIncludesSelf(t *testing.T) {
	cfg, ids := threeCams(t)

	assert.Equal(t, []string{ids[0]}, cfg.GroupPeers(ids[0]),
		"an ungrouped camera is its own only peer")

	require.NoError(t, cfg.SetSyncGroup(ids[0], "stage"))
	require.NoError(t, cfg.SetSyncGroup(ids[2], "stage"))

	assert.ElementsMatch(t, []string{ids[0], ids[2]}, cfg.GroupPeers(ids[0]))
}

func TestLabelIsEditable(t *testing.T) {
	cfg, ids := threeCams(t)
	require.NoError(t, cfg.SetLabel(ids[0], "Chest cam"))

	assert.Equal(t, "Chest cam", cfg.List()[0].Label)
	assert.ErrorIs(t, cfg.SetLabel("nope", "x"), ErrUnknownIngest)
	assert.ErrorIs(t, cfg.SetSyncGroup("nope", "x"), ErrUnknownIngest)
}

func TestEveryCameraGetsItsOwnKeys(t *testing.T) {
	cfg, _ := threeCams(t)
	seen := map[string]bool{}

	for _, ing := range cfg.List() {
		for _, key := range []string{ing.SenderKey, ing.ReceiverKey} {
			assert.False(t, seen[key], "keys must never repeat across cameras")
			seen[key] = true
		}
	}

	assert.Len(t, seen, 6)
}

func TestCodecSetIsOrderedAndFiltered(t *testing.T) {
	cfg, ids := threeCams(t)

	require.NoError(t, cfg.SetCodecs(ids[0], []string{"av1", "nonsense", "h264"}))

	assert.Equal(t, []string{CodecH264, CodecAV1}, cfg.List()[0].Codecs,
		"unknown names are dropped and the rest keep preference order")
}

// An empty set would negotiate nothing and leave a camera that can never
// connect, with no clue why.
func TestEmptyCodecSetFallsBackToH264(t *testing.T) {
	cfg, ids := threeCams(t)

	require.NoError(t, cfg.SetCodecs(ids[0], nil))
	assert.Equal(t, []string{CodecH264}, cfg.List()[0].Codecs)

	require.NoError(t, cfg.SetCodecs(ids[0], []string{"vp9"}))
	assert.Equal(t, []string{CodecH264}, cfg.List()[0].Codecs)
}
