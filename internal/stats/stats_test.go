// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package stats

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestUnknownIngestIsEmpty(t *testing.T) {
	assert.Equal(t, Snapshot{}, New().Of("nope"))
}

func TestBitrateAveragesAcrossSamples(t *testing.T) {
	reg := New()
	reg.Publishing("cam")

	entry := reg.counters["cam"]
	base := time.Now().Add(-2 * time.Second)
	entry.samples = []sample{{at: base, bytes: 0}, {at: base.Add(2 * time.Second), bytes: 250000}}

	// 250 kB over 2 s is 1 Mbit/s.
	assert.InDelta(t, 1000, bitrate(entry.samples), 5)
}

func TestASingleSampleReportsNothing(t *testing.T) {
	assert.Equal(t, 0, bitrate([]sample{{at: time.Now(), bytes: 999}}),
		"one sample cannot describe a rate")
}

func TestStoppedKeepsTheLastState(t *testing.T) {
	reg := New()
	reg.Publishing("cam")
	reg.Observe("cam", 1000, 3, 0)
	reg.Stopped("cam")

	snap := reg.Of("cam")
	assert.False(t, snap.Live)
	assert.Equal(t, uint64(3), snap.Late, "counters survive a publisher dropping off")
	assert.Empty(t, snap.Advice, "an idle camera has nothing to advise")
}

func TestAdviceNamesTheFix(t *testing.T) {
	assert.Contains(t, advise(Snapshot{Live: true, Late: 5}), "Raise the delay")
	assert.Contains(t, advise(Snapshot{Live: true, Dropped: 2}), "Raise the delay")
	assert.Empty(t, advise(Snapshot{Live: true}), "a healthy stream needs no advice")
}

func TestObserveIgnoresAnIngestThatIsNotPublishing(t *testing.T) {
	reg := New()
	reg.Observe("cam", 500, 0, 0)

	assert.False(t, reg.Of("cam").Live, "observing must not resurrect a dead ingest")
}
