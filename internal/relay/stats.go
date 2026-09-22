// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package relay

import (
	"strings"
	"time"

	"github.com/Anywaystv/wagaStrim/internal/dynamicdelay"
)

// Statistics includes corrections made while no new video packets are arriving.
type Statistics struct {
	Playout *dynamicdelay.Status
	Late    uint64
	Dropped uint64
}

// Stats reads video counters and the current shared audio/video delay on demand.
func (r *Relay) Stats(ingestID string) (Statistics, bool) {
	r.mu.RLock()
	stream := r.streams[ingestID]
	r.mu.RUnlock()
	if stream == nil {
		return Statistics{}, false
	}
	stream.mu.RLock()
	defer stream.mu.RUnlock()
	result := Statistics{Late: stream.late, Dropped: stream.dropped}
	for _, buf := range stream.buffers {
		if strings.HasPrefix(strings.ToLower(buf.mime), "video/") {
			late, dropped := buf.Stats()
			result.Late += late
			result.Dropped += dropped
		}
	}
	if len(stream.buffers) > 0 {
		status := stream.delay.Status(time.Now())
		result.Playout = &status
	}

	return result, true
}
