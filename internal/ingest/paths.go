// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package ingest

import (
	"time"

	"github.com/pion/webrtc/v4"
)

// pathPoll is how often the candidate pairs are re-read. Pairs change on the
// order of seconds during gathering and rarely afterwards.
const pathPoll = 2 * time.Second

// watchPairs reports how many candidate pairs are established for a publisher.
//
// This is deliberately a count and not a per-path byte total. pion attributes
// every received packet to the selected pair regardless of which socket it
// arrived on, so bytes cannot be split across paths, and the SRTP replay
// detector discards duplicates without surfacing a count. Reporting how many
// paths exist is honest; reporting how much each carried would not be.
func (s *Server) watchPairs(ingestID string, peer *webrtc.PeerConnection, done <-chan struct{}) {
	tick := time.NewTicker(pathPoll)
	defer tick.Stop()

	for {
		select {
		case <-done:
			return
		case <-tick.C:
			report := peer.GetStats()
			live, total := countPairs(report)
			s.stats.Pairs(ingestID, live, total)
			s.stats.Link(ingestID, roundTrip(report), packetsLost(report))
		}
	}
}

// roundTrip is the last measured round trip on the pair carrying media, in
// milliseconds. It comes from the same report the pair count does, so it costs
// nothing extra, and it is the one number a phone on a bad link is judged by.
func roundTrip(report webrtc.StatsReport) int {
	for _, entry := range report {
		pair, ok := entry.(webrtc.ICECandidatePairStats)
		if !ok || !pair.Nominated {
			continue
		}

		return int(pair.CurrentRoundTripTime * 1000)
	}

	return 0
}

// packetsLost is what the receiver never got and could not have retransmitted in
// time. NACK recovers most of it, so a rising count is the link degrading rather
// than a picture already broken.
func packetsLost(report webrtc.StatsReport) uint64 {
	var lost int32

	for _, entry := range report {
		inbound, ok := entry.(webrtc.InboundRTPStreamStats)
		if !ok {
			continue
		}

		if inbound.PacketsLost > 0 {
			lost += inbound.PacketsLost
		}
	}

	return uint64(lost) //nolint:gosec // negative counts are excluded above.
}

// countPairs returns how many candidate pairs succeeded and how many exist. A
// bonded sender should show more than one succeeding; one means every extra
// interface it tried is unusable, which is worth seeing before a walk outside.
func countPairs(report webrtc.StatsReport) (succeeded, total int) {
	for _, entry := range report {
		pair, ok := entry.(webrtc.ICECandidatePairStats)
		if !ok {
			continue
		}

		total++

		if pair.State == webrtc.StatsICECandidatePairStateSucceeded {
			succeeded++
		}
	}

	return succeeded, total
}
