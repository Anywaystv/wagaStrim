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

// The floor is the product for a phone on cellular. A deployment whose camera
// and player sit in one rack has a different worst case and may lower it, all
// the way to nothing.
func TestALoweredFloorIsHonoredAllTheWayToZero(t *testing.T) {
	cfg := &Config{FloorMS: new(300), Ingests: []Ingest{{DelayMS: 300}, {DelayMS: 120}}}
	cfg.normalise()

	assert.Equal(t, 300, cfg.Ingests[0].DelayMS, "a lowered floor must be honored")
	assert.Equal(t, 300, cfg.Ingests[1].DelayMS, "below the lowered floor still clamps")

	absent := &Config{Ingests: []Ingest{{DelayMS: 300}}}
	absent.normalise()
	assert.Equal(t, DelayFloorMS, absent.Ingests[0].DelayMS, "saying nothing means the product floor")

	// Zero is the setting a wired deployment actually wants, and it is the one
	// value a plain int could not tell apart from having said nothing at all.
	none := &Config{FloorMS: new(0), Ingests: []Ingest{{DelayMS: 0}, {DelayMS: 40}}}
	none.normalise()
	assert.Equal(t, 0, none.Ingests[0].DelayMS, "a stated zero floor must pass a zero delay through")
	assert.Equal(t, 40, none.Ingests[1].DelayMS, "nothing to clamp against once the floor is zero")
	assert.Equal(t, 0, none.Floor(), "the slider has to be told it may reach zero")

	silly := &Config{FloorMS: new(-500), Ingests: []Ingest{{DelayMS: -500}}}
	silly.normalise()
	assert.Equal(t, 0, silly.Ingests[0].DelayMS, "a negative buffer is not a buffer")
}

// A new camera opens at the two second default whatever the floor underneath it
// says, because the floor is a bound and the default is a recommendation.
func TestNewIngestDefaultsToTwoSecondsUnderALoweredFloor(t *testing.T) {
	cfg := &Config{FloorMS: new(0), path: filepath.Join(t.TempDir(), "config.json")}

	ing, err := cfg.AddIngest("wired")
	require.NoError(t, err)

	assert.Equal(t, DelayDefaultMS, ing.DelayMS, "the default does not follow the floor down")
}
