// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package ingest

import (
	"sync/atomic"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/stun/v3"
	"github.com/pion/webrtc/v4"
)

const pathSilence = 2 * time.Second

type pathFailover struct {
	selected atomic.Pointer[webrtc.ICECandidatePair]
	observed *ice.CandidatePair // Only accessed by Pion's serialized binding handler.
}

// Pion authenticates the request before calling this handler. A successful
// round trip is also required before moving the return path to another address.
func (f *pathFailover) binding(_ *stun.Message, _, _ ice.Candidate, pair *ice.CandidatePair) bool {
	selected := f.selected.Load()
	if matchesSelection(pair, selected) {
		f.observed = pair

		return false
	}
	if !matchesSelection(f.observed, selected) || pair == nil || pair.CurrentRoundTripTime() <= 0 {
		return false
	}
	last := f.observed.Remote.LastReceived()
	if last.IsZero() || time.Since(last) < pathSilence {
		return false
	}

	return true
}

func matchesSelection(pair *ice.CandidatePair, selected *webrtc.ICECandidatePair) bool {
	return pair != nil && selected != nil && selected.Local != nil && selected.Remote != nil &&
		pair.Local.Address() == selected.Local.Address && pair.Local.Port() == int(selected.Local.Port) &&
		pair.Remote.Address() == selected.Remote.Address && pair.Remote.Port() == int(selected.Remote.Port)
}
