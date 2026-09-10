// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

// Package stats tracks ingest health and recent problems for the dashboard.
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

	// Codec is the negotiated codec, not the configured list of allowed codecs.
	Codec string `json:"codec,omitempty"`

	// RTT on the nominated pair, and packets the receiver never got. Both come
	// from the report the pair count is already reading.
	RTT  int    `json:"rttMs,omitempty"`
	Lost uint64 `json:"packetsLost"`

	// How many candidate pairs succeeded, out of how many exist. Not how much
	// each carried: pion credits every received packet to the selected pair, so
	// per-path traffic cannot be measured. See internal/ingest/paths.go.
	PathsLive  int `json:"pathsLive"`
	PathsTotal int `json:"pathsTotal"`
}

type sample struct {
	at    time.Time
	bytes uint64
}

type counter struct {
	samples    []sample
	path       string
	codec      string
	pathsLive  int
	pathsTotal int

	// Count candidate changes even when they do not require a reconnect.
	switches int

	rtt     int
	lost    uint64
	total   uint64
	started time.Time
	live    bool
	late    uint64
	dropped uint64

	// Expire advice independently for late packets and skips; lifetime counts
	// must not keep an old problem visible after recovery.
	lastMoved   time.Time
	prevBad     uint64
	lastSkipped time.Time
	prevSkipped uint64
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
		fresh.codec = previous.codec
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

	// Pair selection can precede Publishing, so create the counter if needed.
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

// Codec records what the publisher negotiated. Like Path it may arrive before
// the connection reports itself connected, so a missing counter is created
// rather than dropped.
func (r *Registry) Codec(ingestID, codec string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, ok := r.counters[ingestID]
	if !ok {
		entry = &counter{}
		r.counters[ingestID] = entry
	}

	entry.codec = codec
}

// Pairs records how many candidate pairs are usable.
func (r *Registry) Pairs(ingestID string, live, total int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if entry, ok := r.counters[ingestID]; ok {
		entry.pathsLive, entry.pathsTotal = live, total
	}
}

// Link records what the ICE and RTP reports say about the path itself, as
// opposed to what the buffer says about the media on it.
func (r *Registry) Link(ingestID string, rttMS int, lost uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if entry, ok := r.counters[ingestID]; ok {
		entry.rtt = rttMS
		entry.lost = lost
	}
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
	entry.total = total
	entry.late = late
	entry.dropped = dropped

	// Sampling every packet buys no accuracy over a five second average and
	// churns the slice thousands of times a second.
	if len(entry.samples) > 0 && now.Sub(entry.samples[len(entry.samples)-1].at) < sampleEvery {
		return
	}

	if bad := late + dropped; bad > entry.prevBad {
		entry.prevBad = bad
		entry.lastMoved = now
	}

	if dropped > entry.prevSkipped {
		entry.prevSkipped = dropped
		entry.lastSkipped = now
	}
	entry.samples = append(entry.samples, sample{at: now, bytes: total})

	cutoff := now.Add(-window)
	for len(entry.samples) > 1 && entry.samples[0].at.Before(cutoff) {
		entry.samples = entry.samples[1:]
	}
}

// Report returns one snapshot per camera, which is what both the settings page
// and a deployment watching from another machine ask for.
func (r *Registry) Report(ingestIDs []string) map[string]Snapshot {
	out := make(map[string]Snapshot, len(ingestIDs))

	for _, id := range ingestIDs {
		out[id] = r.Of(id)
	}

	return out
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
		Live:       entry.live,
		Bitrate:    bitrate(entry.samples, time.Now()),
		Late:       entry.late,
		Dropped:    entry.dropped,
		RTT:        entry.rtt,
		Lost:       entry.lost,
		Path:       entry.path,
		Codec:      entry.codec,
		Switches:   entry.switches,
		PathsLive:  entry.pathsLive,
		PathsTotal: entry.pathsTotal,
	}

	if entry.live {
		snap.Since = int(time.Since(entry.started).Seconds())
	}

	snap.Advice = advise(snap,
		time.Since(entry.lastMoved) < adviceWindow,
		time.Since(entry.lastSkipped) < adviceWindow)

	return snap
}

// bitrate averages across the window rather than between the last two samples,
// which would swing wildly on a variable-bitrate encoder.
func bitrate(samples []sample, now time.Time) int {
	if len(samples) < 2 {
		return 0
	}

	first, last := samples[0], samples[len(samples)-1]

	// Expire the rate on wall time even if ICE stays connected without media.
	if now.Sub(last.at) > window {
		return 0
	}

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
