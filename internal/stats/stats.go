// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

// Package stats keeps a short rolling view of each ingest. It exists so the UI
// can say what is wrong in a sentence, not so it can draw a graph.
package stats

import (
	"sync"
	"time"
)

// window is how far back the bitrate average reaches. Long enough that a single
// slow frame does not swing it, short enough to notice a link degrading.
const (
	window      = 5 * time.Second
	sampleEvery = 250 * time.Millisecond
)

// Snapshot is what the UI renders for one ingest.
type Snapshot struct {
	Live     bool   `json:"live"`
	Bitrate  int    `json:"bitrateKbps"`
	Late     uint64 `json:"late"`
	Dropped  uint64 `json:"dropped"`
	Since    int    `json:"liveSeconds"`
	Advice   string `json:"advice,omitempty"`
	Path     string `json:"path,omitempty"`
	Switches int    `json:"switches"`
}

type sample struct {
	at    time.Time
	bytes uint64
}

type counter struct {
	samples []sample
	path    string

	// Renomination re-homes a stream onto a better candidate without a
	// reconnect. Counting the moves is how anyone can tell it did anything: the
	// stream simply keeps working, which looks identical to nothing happening.
	switches int

	total   uint64
	started time.Time
	live    bool
	late    uint64
	dropped uint64

	// Advice is driven by recent movement, not the lifetime total. A single late
	// packet an hour ago must not pin "raise the delay" on screen forever.
	lastMoved time.Time
	prevBad   uint64
}

// Registry holds one counter per ingest.
type Registry struct {
	mu       sync.Mutex
	counters map[string]*counter
}

// New builds an empty registry.
func New() *Registry {
	return &Registry{counters: map[string]*counter{}}
}

// Publishing marks an ingest live and resets its window.
func (r *Registry) Publishing(ingestID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Keep the path across a reconnect so the count survives; a phone that
	// re-homes twice through one tunnel is the thing worth seeing.
	previous := r.counters[ingestID]
	fresh := &counter{started: time.Now(), live: true}

	if previous != nil {
		fresh.path, fresh.switches = previous.path, previous.switches
	}

	r.counters[ingestID] = fresh
}

// Stopped marks an ingest idle but keeps the counters, so the last known state
// is still visible after a phone drops off.
func (r *Registry) Stopped(ingestID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if entry, ok := r.counters[ingestID]; ok {
		entry.live = false
		entry.samples = nil
	}
}

// Path records the candidate pair now carrying the stream. The first pair is
// the connection being established, not a switch, so it is not counted.
func (r *Registry) Path(ingestID, path string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// A candidate pair is selected before the connection reports itself
	// connected, so the counter may not exist yet. Dropping the first path here
	// is how the whole feature silently reported nothing.
	entry, ok := r.counters[ingestID]
	if !ok {
		entry = &counter{}
		r.counters[ingestID] = entry
	}

	if entry.path == path {
		return
	}

	if entry.path != "" {
		entry.switches++
	}

	entry.path = path
}

// Observe records bytes arriving and the buffer's own counters.
func (r *Registry) Observe(ingestID string, total, late, dropped uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, ok := r.counters[ingestID]
	if !ok {
		return
	}

	now := time.Now()

	// Sampling every packet buys no accuracy over a five second average and
	// churns the slice thousands of times a second.
	if len(entry.samples) > 0 && now.Sub(entry.samples[len(entry.samples)-1].at) < sampleEvery {
		entry.total = total
		entry.late = late
		entry.dropped = dropped

		return
	}

	entry.total = total
	entry.late = late
	entry.dropped = dropped

	if bad := late + dropped; bad > entry.prevBad {
		entry.prevBad = bad
		entry.lastMoved = now
	}
	entry.samples = append(entry.samples, sample{at: now, bytes: total})

	cutoff := now.Add(-window)
	for len(entry.samples) > 1 && entry.samples[0].at.Before(cutoff) {
		entry.samples = entry.samples[1:]
	}
}

// Of returns the current view of one ingest.
func (r *Registry) Of(ingestID string) Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, ok := r.counters[ingestID]
	if !ok {
		return Snapshot{}
	}

	snap := Snapshot{
		Live:     entry.live,
		Bitrate:  bitrate(entry.samples),
		Late:     entry.late,
		Dropped:  entry.dropped,
		Path:     entry.path,
		Switches: entry.switches,
	}

	if entry.live {
		snap.Since = int(time.Since(entry.started).Seconds())
	}

	snap.Advice = advise(snap, time.Since(entry.lastMoved) < adviceWindow, entry.dropped > 0)

	return snap
}

// bitrate averages across the window rather than between the last two samples,
// which would swing wildly on a variable-bitrate encoder.
func bitrate(samples []sample) int {
	if len(samples) < 2 {
		return 0
	}

	first, last := samples[0], samples[len(samples)-1]

	seconds := last.at.Sub(first.at).Seconds()
	if seconds <= 0 {
		return 0
	}

	return int(float64(last.bytes-first.bytes) * 8 / seconds / 1000)
}

// adviceWindow is how long after the last bad packet the advice stays up. Long
// enough to read, short enough that a stream which recovered stops nagging.
const adviceWindow = 30 * time.Second

// advise turns the counters into the sentence the UI shows. Numbers alone make
// a streamer guess; the point of collecting them is to say what to do.
func advise(snap Snapshot, recent, skipping bool) string {
	switch {
	case !snap.Live, !recent:
		return ""
	case skipping:
		return "Skipping to keyframes to keep up. Raise the delay for this camera."
	default:
		return "Packets are arriving after their slot. Raise the delay for this camera."
	}
}
