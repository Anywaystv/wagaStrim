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
	live := Snapshot{Live: true}

	assert.Contains(t, advise(live, true, false), "Raise the delay")
	assert.Contains(t, advise(live, true, true), "Skipping to keyframes")
	assert.Empty(t, advise(live, false, false), "a healthy stream needs no advice")
	assert.Empty(t, advise(Snapshot{}, true, true), "an idle camera has nothing to advise")
}

// A single late packet must not pin advice on screen for the rest of the stream.
func TestAdviceClearsAfterRecovery(t *testing.T) {
	reg := New()
	reg.Publishing("cam")
	reg.Observe("cam", 1000, 1, 0)

	assert.NotEmpty(t, reg.Of("cam").Advice, "a fresh problem is worth saying")

	reg.counters["cam"].lastMoved = time.Now().Add(-adviceWindow - time.Second)

	assert.Empty(t, reg.Of("cam").Advice, "a recovered stream must stop nagging")
	assert.Equal(t, uint64(1), reg.Of("cam").Late, "the count itself still stands")
}

// Sampling every packet would churn the slice thousands of times a second.
func TestSamplesAreRateLimited(t *testing.T) {
	reg := New()
	reg.Publishing("cam")

	for i := range 100 {
		reg.Observe("cam", uint64(i)*1000, 0, 0)
	}

	assert.LessOrEqual(t, len(reg.counters["cam"].samples), 2,
		"a burst of packets must not become a burst of samples")
	assert.Equal(t, uint64(99000), reg.counters["cam"].total,
		"the running total still tracks every packet")
}

func TestObserveIgnoresAnIngestThatIsNotPublishing(t *testing.T) {
	reg := New()
	reg.Observe("cam", 500, 0, 0)

	assert.False(t, reg.Of("cam").Live, "observing must not resurrect a dead ingest")
}
