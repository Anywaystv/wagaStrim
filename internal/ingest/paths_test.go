// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package ingest

import (
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCountPairsSeparatesSucceededFromTried(t *testing.T) {
	report := webrtc.StatsReport{
		"a": webrtc.ICECandidatePairStats{State: webrtc.StatsICECandidatePairStateSucceeded},
		"b": webrtc.ICECandidatePairStats{State: webrtc.StatsICECandidatePairStateSucceeded},
		"c": webrtc.ICECandidatePairStats{State: webrtc.StatsICECandidatePairStateFailed},
		"d": webrtc.ICECandidatePairStats{State: webrtc.StatsICECandidatePairStateInProgress},
		"e": webrtc.OutboundRTPStreamStats{},
	}

	live, total := countPairs(report)

	assert.Equal(t, 2, live, "only succeeded pairs can carry media")
	assert.Equal(t, 4, total, "non-pair entries must not be counted")
}

func TestCountPairsOnAnEmptyReport(t *testing.T) {
	live, total := countPairs(webrtc.StatsReport{})

	assert.Zero(t, live)
	assert.Zero(t, total)
}

// The load-bearing claim of the bonded receive path is that a real publisher
// establishes more than one usable pair. If only one ever succeeds, a spraying
// sender would have nowhere to spray.
func TestARealPublisherEstablishesPairs(t *testing.T) {
	srv, ing := newTestServer(t)
	peer, track := publisher(t)

	answer, resource, err := srv.Publish(ing.SenderKey, offerFrom(t, peer))
	require.NoError(t, err)
	require.NoError(t, peer.SetRemoteDescription(
		webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}))

	session, ok := srv.session(resource)
	require.True(t, ok)

	frame := []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x00, 0x1f, 0x00, 0x00, 0x00, 0x01, 0x65, 0x88}

	require.Eventually(t, func() bool {
		_ = track.WriteSample(media(frame))

		return session.Bytes() > 0
	}, 15*time.Second, 50*time.Millisecond)

	require.Eventually(t, func() bool {
		return srv.stats.Of(ing.ID).PathsTotal > 0
	}, 10*time.Second, 200*time.Millisecond, "candidate pairs must be reported for a live publisher")

	snap := srv.stats.Of(ing.ID)
	assert.Positive(t, snap.PathsLive, "at least the carrying pair must be reported as succeeded")
	assert.GreaterOrEqual(t, snap.PathsTotal, snap.PathsLive)
}

func TestRoundTripComesFromTheNominatedPair(t *testing.T) {
	report := webrtc.StatsReport{
		"a": webrtc.ICECandidatePairStats{Nominated: false, CurrentRoundTripTime: 0.5},
		"b": webrtc.ICECandidatePairStats{Nominated: true, CurrentRoundTripTime: 0.042},
	}

	assert.Equal(t, 42, roundTrip(report), "the pair carrying media is the one whose latency counts")
	assert.Zero(t, roundTrip(webrtc.StatsReport{}), "nothing measured yet is not a round trip of zero milliseconds")
}

func TestPacketsLostSumsInboundStreams(t *testing.T) {
	report := webrtc.StatsReport{
		"video": webrtc.InboundRTPStreamStats{PacketsLost: 12},
		"audio": webrtc.InboundRTPStreamStats{PacketsLost: 3},
		// pion reports a negative count when more packets arrived than were
		// expected, which duplicates from a bonded sender can do.
		"odd":  webrtc.InboundRTPStreamStats{PacketsLost: -4},
		"pair": webrtc.ICECandidatePairStats{},
	}

	assert.Equal(t, uint64(15), packetsLost(report))
}
