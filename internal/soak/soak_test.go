// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

// Package soak runs media through the whole path for a long time and watches
// what grows. The premise of this product is a process that stays up for weeks,
// and nothing else in the test suite would notice a slow leak.
package soak

import (
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/Anywaystv/wagaStrim/internal/config"
	"github.com/Anywaystv/wagaStrim/internal/egress"
	"github.com/Anywaystv/wagaStrim/internal/ingest"
	"github.com/Anywaystv/wagaStrim/internal/relay"
	"github.com/Anywaystv/wagaStrim/internal/stats"
	"github.com/pion/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const maxGoroutineGrowth = 20

// A leak keeps climbing; a buffer filling up stops. Comparing the end against a
// cold baseline cannot tell those apart, and neither can splitting the run in
// half, because warmup is not instant: the NACK history holds 4096 packets in
// each direction and measured about two and a half minutes to reach depth at
// roughly 12 MB. So a fixed warmup window is discarded and only what follows is
// compared, which means a useful run has to be several times that window.
const (
	warmup           = 3 * time.Minute
	minSoakMinutes   = 8
	maxLateHeapRatio = 1.10
)

// sample is one reading of what a leak would move.
type sample struct {
	at         time.Time
	goroutines int
	heapBytes  uint64
}

func mean(values []uint64) uint64 {
	var total uint64
	for _, value := range values {
		total += value
	}

	return total / uint64(len(values)) //nolint:gosec // len is never negative.
}

func take() sample {
	var mem runtime.MemStats

	runtime.GC()
	runtime.ReadMemStats(&mem)

	return sample{at: time.Now(), goroutines: runtime.NumGoroutine(), heapBytes: mem.HeapAlloc}
}

// TestSoak carries real media for WAGASTRIM_SOAK minutes and asserts that
// goroutines and heap are flat at the end. It is skipped without that variable,
// because a useful run is minutes at least and CI should not pay for it on
// every push.
func TestSoak(t *testing.T) {
	minutes := os.Getenv("WAGASTRIM_SOAK")
	if minutes == "" {
		t.Skip("set WAGASTRIM_SOAK to a number of minutes to run this")
	}

	duration, err := strconv.Atoi(minutes)
	require.NoError(t, err, "WAGASTRIM_SOAK must be a number of minutes")

	cam := config.Ingest{
		ID:          "soak",
		Label:       "Soak",
		SenderKey:   config.SenderPrefix + "00000000000000000000000000000001",
		ReceiverKey: config.ReceiverPrefix + "00000000000000000000000000000002",
		Codecs:      []string{config.CodecH264},
		DelayMS:     config.DelayFloorMS,
	}
	cfg := &config.Config{Ingests: []config.Ingest{cam}}

	engine, mux, err := ingest.NewSettingEngine(0)
	require.NoError(t, err)

	defer func() { _ = mux.Close() }()

	log := logging.NewDefaultLoggerFactory().NewLogger("soak")
	hub := relay.New()
	counters := stats.New(hub)

	whip, err := ingest.NewServer(cfg, log, engine, hub, counters, func(string) {})
	require.NoError(t, err)

	defer whip.Close()

	whep := egress.NewServer(cfg, log, whip.API(), hub)
	defer whep.Close()

	writeFrame := attachPublisher(t, whip, &cam)
	attachSubscriber(t, whep, &cam, writeFrame)

	require.GreaterOrEqual(t, duration, minSoakMinutes,
		"a shorter run cannot tell a leak from buffers filling")

	baseline := take()
	t.Logf("baseline: %d goroutines, %d KB heap", baseline.goroutines, baseline.heapBytes/1024)

	started := time.Now()
	total := time.Duration(duration) * time.Minute
	deadline := started.Add(total)
	settled := started.Add(warmup)
	midpoint := settled.Add((total - warmup) / 2)

	var early, late []uint64
	frames := time.NewTicker(33 * time.Millisecond)

	defer frames.Stop()

	report := time.NewTicker(30 * time.Second)
	defer report.Stop()

	for time.Now().Before(deadline) {
		select {
		case <-frames.C:
			writeFrame()
		case <-report.C:
			now := take()
			t.Logf("+%s: %d goroutines, %d KB heap, %d kbps",
				now.at.Sub(started).Round(time.Second), now.goroutines,
				now.heapBytes/1024, counters.Of(cam.ID).Bitrate)

			switch {
			case now.at.Before(settled):
			case now.at.Before(midpoint):
				early = append(early, now.heapBytes)
			default:
				late = append(late, now.heapBytes)
			}
		}
	}

	final := take()
	t.Logf("final: %d goroutines, %d KB heap", final.goroutines, final.heapBytes/1024)

	assert.LessOrEqual(t, final.goroutines-baseline.goroutines, maxGoroutineGrowth,
		"goroutines climbed, which is a leak rather than settling")
	assert.Positive(t, counters.Of(cam.ID).Bitrate, "media must still be flowing at the end")

	require.NotEmpty(t, early, "no samples after warmup")
	require.NotEmpty(t, late, "no samples in the final stretch")

	firstHalf, secondHalf := mean(early), mean(late)
	t.Logf("heap after warmup: %d KB then %d KB", firstHalf/1024, secondHalf/1024)

	assert.LessOrEqual(t, float64(secondHalf), float64(firstHalf)*maxLateHeapRatio,
		"heap was still climbing after warmup, which is a leak")
}
