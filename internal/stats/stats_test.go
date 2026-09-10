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
	assert.InDelta(t, 1000, bitrate(entry.samples, time.Now()), 5)
}

func TestASingleSampleReportsNothing(t *testing.T) {
	assert.Equal(t, 0, bitrate([]sample{{at: time.Now(), bytes: 999}}, time.Now()),
		"one sample cannot describe a rate")
}

// A phone that stops sending without dropping its ICE connection reads as live.
// If its last rate stood forever, nothing watching could tell that camera from a
// working one, and the fallback scene would never fire.
func TestBitrateFallsToZeroWhenMediaStops(t *testing.T) {
	base := time.Now().Add(-time.Minute)
	samples := []sample{{at: base, bytes: 0}, {at: base.Add(2 * time.Second), bytes: 250000}}

	assert.InDelta(t, 1000, bitrate(samples, base.Add(2*time.Second)), 5,
		"while packets were arriving the rate is the rate")
	assert.Equal(t, 0, bitrate(samples, time.Now()),
		"a window with nothing in it is not a bitrate")
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
		reg.Observe("cam", uint64(i)*1000, uint64(i), uint64(i)*2)
	}

	assert.LessOrEqual(t, len(reg.counters["cam"].samples), 2,
		"a burst of packets must not become a burst of samples")
	assert.Equal(t, uint64(99), reg.Of("cam").Late, "late counts update between bitrate samples")
	assert.Equal(t, uint64(198), reg.Of("cam").Dropped, "drop counts update between bitrate samples")
}

func TestObserveIgnoresAnIngestThatIsNotPublishing(t *testing.T) {
	reg := New()
	reg.Observe("cam", 500, 0, 0)

	assert.False(t, reg.Of("cam").Live, "observing must not resurrect a dead ingest")
}

// The first pair is the connection forming, not a re-home. Counting it would
// report a network change on every stream that ever worked.
func TestFirstPathIsNotASwitch(t *testing.T) {
	reg := New()
	reg.Publishing("cam")
	reg.Path("cam", "direct over udp via 192.168.1.2")

	assert.Zero(t, reg.Of("cam").Switches)
	assert.Equal(t, "direct over udp via 192.168.1.2", reg.Of("cam").Path)
}

func TestRehomingCounts(t *testing.T) {
	reg := New()
	reg.Publishing("cam")
	reg.Path("cam", "local network over udp via 192.168.1.2")
	reg.Path("cam", "through NAT over udp via 192.168.1.2")

	assert.Equal(t, 1, reg.Of("cam").Switches, "Wi-Fi to cellular is one change")
	assert.Contains(t, reg.Of("cam").Path, "through NAT")
}

func TestRepeatingThePathIsNotAChange(t *testing.T) {
	reg := New()
	reg.Publishing("cam")

	for range 5 {
		reg.Path("cam", "direct over udp via 10.0.0.1")
	}

	assert.Zero(t, reg.Of("cam").Switches, "renomination re-confirming a pair is not a move")
}

// A phone that reconnects has still moved networks; losing the count would hide
// exactly the flapping worth seeing.
func TestSwitchCountSurvivesAReconnect(t *testing.T) {
	reg := New()
	reg.Publishing("cam")
	reg.Path("cam", "a")
	reg.Path("cam", "b")
	reg.Stopped("cam")
	reg.Publishing("cam")

	assert.Equal(t, 1, reg.Of("cam").Switches)
}

// The codec is negotiated once per session but read on every poll, so it has to
// outlive the connection that discovered it.
func TestCodecSurvivesAReconnect(t *testing.T) {
	reg := New()
	reg.Publishing("cam")
	reg.Codec("cam", "H.264")
	reg.Stopped("cam")
	reg.Publishing("cam")

	assert.Equal(t, "H.264", reg.Of("cam").Codec, "the last known codec is still the answer")
}

// A camera that skipped keyframes once at a lower target keeps arriving late
// while it settles. Reading the lifetime drop count kept "skipping to keyframes"
// on screen for a buffer that had not skipped in minutes.
func TestSkippingAdviceFollowsRecentDropsNotTheLifetimeTotal(t *testing.T) {
	reg := New()
	reg.Publishing("cam")
	reg.Observe("cam", 1000, 10, 5)

	assert.Contains(t, reg.Of("cam").Advice, "Skipping to keyframes")

	reg.mu.Lock()
	entry := reg.counters["cam"]
	entry.lastSkipped = time.Now().Add(-2 * adviceWindow)
	// The sample has to age too. A second Observe inside the sampling interval
	// returns at the rate limit without ever reaching the branch under test.
	entry.samples[len(entry.samples)-1].at = time.Now().Add(-2 * sampleEvery)
	reg.mu.Unlock()

	// The same skip count as before: nothing new was dropped, only arrived late.
	reg.Observe("cam", 2000, 20, 5)

	assert.Equal(t, "Packets are arriving after their slot. Raise the delay for this camera.",
		reg.Of("cam").Advice, "late packets alone must not report a skip that stopped")
}
