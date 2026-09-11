// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package config

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pushed builds the shape a deployment sends: keys it minted itself.
func pushed(id string, suffix byte) Ingest {
	return Ingest{
		ID:          id,
		Label:       id,
		SenderKey:   SenderPrefix + strings.Repeat(string(suffix), 32),
		ReceiverKey: ReceiverPrefix + strings.Repeat(string(suffix+1), 32),
		Codecs:      []string{CodecH264},
		DelayMS:     DelayDefaultMS,
	}
}

func TestReplaceIngestsTakesSuppliedKeys(t *testing.T) {
	cfg := &Config{path: t.TempDir() + "/config.json"}

	stale, err := cfg.ReplaceIngests([]Ingest{pushed("cam1", 'a'), pushed("cam2", 'c')})
	require.NoError(t, err)
	assert.Empty(t, stale, "nothing was running, so nothing had to be dropped")

	list := cfg.List()
	require.Len(t, list, 2)
	assert.Equal(t, SenderPrefix+strings.Repeat("a", 32), list[0].SenderKey,
		"the key a deployment supplied is the key a phone was given")

	found, role := cfg.Resolve(list[1].ReceiverKey)
	assert.Equal(t, RoleReceiver, role)
	assert.Equal(t, "cam2", found.ID)
}

// A replaced machine replays the same document. Nothing may be torn down for
// that, or every reprovision would interrupt a camera that is still publishing.
func TestReplayingTheSameListDropsNothing(t *testing.T) {
	cfg := &Config{path: t.TempDir() + "/config.json"}
	list := []Ingest{pushed("cam1", 'a')}

	_, err := cfg.ReplaceIngests(list)
	require.NoError(t, err)

	stale, err := cfg.ReplaceIngests(list)
	require.NoError(t, err)
	assert.Empty(t, stale)
}

func TestReplaceReportsWhatCanNoLongerBeTrusted(t *testing.T) {
	cfg := &Config{path: t.TempDir() + "/config.json"}

	_, err := cfg.ReplaceIngests([]Ingest{pushed("cam1", 'a'), pushed("cam2", 'c'), pushed("cam3", 'e')})
	require.NoError(t, err)

	rotated := pushed("cam1", '1')
	recodec := pushed("cam2", 'c')
	recodec.Codecs = []string{CodecH265}

	stale, err := cfg.ReplaceIngests([]Ingest{rotated, recodec})
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{"cam1", "cam2", "cam3"}, stale,
		"a rotated key, a changed codec set and a deleted camera all end their sessions")
}

func TestReplaceRefusesAListItCannotResolve(t *testing.T) {
	cfg := &Config{path: t.TempDir() + "/config.json"}

	short := pushed("cam1", 'a')
	short.SenderKey = SenderPrefix + "abcd"

	_, err := cfg.ReplaceIngests([]Ingest{short})
	assert.ErrorIs(t, err, ErrBadKey, "a key that cannot have been minted here never resolves")

	unprefixed := pushed("cam1", 'a')
	unprefixed.ReceiverKey = strings.Repeat("b", 32)

	_, err = cfg.ReplaceIngests([]Ingest{unprefixed})
	assert.ErrorIs(t, err, ErrBadKey, "a key with no role prefix cannot say which half it is")

	shared := pushed("cam2", 'a')

	_, err = cfg.ReplaceIngests([]Ingest{pushed("cam1", 'a'), shared})
	assert.ErrorIs(t, err, ErrDuplicateKey, "one key resolving to two cameras is ambiguous")

	_, err = cfg.ReplaceIngests([]Ingest{pushed("cam1", 'a'), pushed("cam1", 'c')})
	assert.ErrorIs(t, err, ErrBadIngest, "two cameras cannot share an identifier")

	nameless := pushed("", 'a')

	_, err = cfg.ReplaceIngests([]Ingest{nameless})
	assert.ErrorIs(t, err, ErrBadIngest)

	assert.Empty(t, cfg.List(), "a refused list must not have been half applied")
}

// Explicit deployment minima still apply; the default permits zero delay.
func TestALoweredFloorIsHonoredAllTheWayToZero(t *testing.T) {
	for _, tc := range []struct {
		name        string
		floor       *int
		delay, want int
	}{
		{"at floor", new(300), 300, 300},
		{"below floor", new(300), 120, 300},
		{"omitted floor", nil, 300, 300},
		{"zero delay", new(0), 0, 0},
		{"above zero", new(0), 40, 40},
		{"negative floor", new(-500), -500, 0},
		{"excessive floor", new(99999), 500, DelayMaxMS},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{FloorMS: tc.floor, Ingests: []Ingest{{DelayMS: tc.delay}}}
			cfg.normalise()
			assert.Equal(t, tc.want, cfg.Ingests[0].DelayMS)
			if tc.floor != nil && *tc.floor == 0 {
				assert.Zero(t, cfg.Floor(), "the slider must allow zero")
			}
		})
	}
}

// A new camera opens at the two second default whatever the floor underneath it
// says, because the floor is a bound and the default is a recommendation.
func TestNewIngestDefaultsToTwoSecondsUnderALoweredFloor(t *testing.T) {
	cfg := &Config{FloorMS: new(0), path: filepath.Join(t.TempDir(), "config.json")}

	ing, err := cfg.AddIngest("wired")
	require.NoError(t, err)

	assert.Equal(t, DelayDefaultMS, ing.DelayMS, "the default does not follow the floor down")
}
