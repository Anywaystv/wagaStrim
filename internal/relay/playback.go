// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package relay

import (
	"math"
	"strings"
	"time"

	"github.com/Anywaystv/wagaStrim/internal/dynamicdelay"
)

// SenderReport keeps the publisher's clock mapping independently of packet arrival.
func (b *Buffer) SenderReport(timestamp uint32, ntp uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delta := ntp - b.senderNTP
	if ntp == 0 || (b.senderNTP != 0 && (delta == 0 || delta > math.MaxInt64)) {
		return
	}
	// A reset can fall between reports while still advancing the report's counter.
	ticks := rtpDelta(timestamp, b.senderRTP)
	elapsed := time.Duration(delta>>32)*time.Second + time.Duration(delta&math.MaxUint32)*time.Second/(1<<32)
	drift := elapsed - time.Duration(ticks)*time.Second/time.Duration(b.clockRate)
	if b.senderNTP != 0 && (ticks < 0 || drift.Abs() > correctionMargin(b.target)) {
		b.clockReset = true
	}
	b.senderRTP, b.senderNTP = timestamp, ntp
}

func (b *Buffer) senderTimeMS(timestamp uint32) float64 {
	return float64(b.senderNTP>>32)*1000 + float64(b.senderNTP&math.MaxUint32)*1000/(1<<32) +
		float64(rtpDelta(timestamp, b.senderRTP))*1000/float64(b.clockRate)
}

// Playback snapshots delay and queued media so a late-joining player keeps the track offset.
// No media parsing or extra work is needed in the packet forwarding loop.
func (r *Relay) Playback(ingestID string) dynamicdelay.Playback {
	var playback dynamicdelay.Playback
	r.mu.RLock()
	stream := r.streams[ingestID]
	r.mu.RUnlock()
	if stream == nil {
		return playback
	}
	stream.mu.RLock()
	defer stream.mu.RUnlock()
	playback.Clocks = make([]dynamicdelay.Clock, 0, len(stream.buffers))
	state := stream.delay.State()
	if state != nil {
		playback.CurrentDelayMS = float64(state.Delay(time.Now())) / float64(time.Millisecond)
	}
	senderClocks := true
	for _, buf := range stream.buffers {
		buf.mu.Lock()
		senderClocks = senderClocks && buf.senderNTP != 0
	}
	for _, buf := range stream.buffers {
		if state == nil {
			playback.CurrentDelayMS = float64(buf.target) / float64(time.Millisecond)
		}
		if buf.based {
			playback.Clocks = append(playback.Clocks, buf.playbackClockLocked(senderClocks))
		}
		buf.mu.Unlock()
	}

	return playback
}

func (b *Buffer) playbackClockLocked(senderClocks bool) dynamicdelay.Clock {
	stamp := b.baseRTP
	if senderClocks {
		stamp = b.senderRTP
	}
	if len(b.queue) > 0 {
		stamp = b.queue[0].pkt.Timestamp
	}
	kind, _, _ := strings.Cut(b.mime, "/")
	var referenceMS float64
	if senderClocks {
		// Both reports use the publisher's NTP epoch, even if their network
		// arrivals differ. Do not mix that epoch with arrival-based fallback.
		referenceMS = b.senderTimeMS(stamp)
	} else {
		// Normalize jumps that one track has applied before the other.
		reference := b.playoutOf(stamp).Add(-b.target - b.shift)
		referenceMS = float64(reference.UnixMicro()) / float64(time.Millisecond/time.Microsecond)
	}

	return dynamicdelay.Clock{
		Kind: strings.ToLower(kind), MIME: b.mime, Rate: b.clockRate, Timestamp: stamp,
		ReferenceMS: referenceMS, Epoch: b.clockEpoch,
	}
}
