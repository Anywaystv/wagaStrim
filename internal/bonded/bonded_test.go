// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

// Package bonded verifies that SRTP sent over multiple real sockets reaches
// the subscriber as one merged, deduplicated stream.
package bonded

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/Anywaystv/wagaStrim/internal/config"
	"github.com/Anywaystv/wagaStrim/internal/dynamicdelay"
	"github.com/Anywaystv/wagaStrim/internal/egress"
	"github.com/Anywaystv/wagaStrim/internal/ingest"
	"github.com/Anywaystv/wagaStrim/internal/relay"
	"github.com/Anywaystv/wagaStrim/internal/stats"
	"github.com/pion/ice/v4"
	"github.com/pion/interceptor"
	"github.com/pion/logging"
	"github.com/pion/rtp"
	"github.com/pion/transport/v4/stdnet"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// measured is the run the assertions are made over. It is long enough to
	// cross the pacing of ICE checks and short enough to keep the test quick.
	measured = 120

	// The H.264 clock, and one 30 fps frame in it.
	clockRate = 90000
	frameStep = clockRate / 30

	// pace has to match the timestamp step. Sending faster than the media clock
	// grows the buffer past its target, and drift correction then drops the
	// packets this test is counting.
	pace = time.Second / 30
)

// arrivals records what the OBS side of the relay actually saw. A sequence
// number seen twice is a deduplication failure, and one never seen is a packet
// the merge dropped.
type arrivals struct {
	mu   sync.Mutex
	seen map[uint16]int
}

func (a *arrivals) add(seq uint16) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.seen[seq]++
}

// spanOf reports how many of the sequence numbers from first were seen at all,
// and how many of them arrived more than once.
func (a *arrivals) spanOf(first uint16, count int) (present, repeated int) {
	a.mu.Lock()
	defer a.mu.Unlock()

	for i := range count {
		times := a.seen[first+uint16(i)]
		if times > 0 {
			present++
		}

		if times > 1 {
			repeated += times - 1
		}
	}

	return present, repeated
}

// outboundIP is the address traffic to the internet would leave from. It is the
// second path: the sender gathers a candidate on it and on loopback, and the
// receiver's mux binds both. Connecting a UDP socket sends nothing.
func outboundIP(t *testing.T) net.IP {
	t.Helper()

	// TEST-NET-1, which is unrouteable by definition, so nothing is contacted.
	probe, err := (&net.Dialer{}).DialContext(t.Context(), "udp4", "192.0.2.1:9")
	if err != nil {
		t.Skip("no route off this machine, so there is only one path to bond")
	}

	defer func() { _ = probe.Close() }()

	addr, ok := probe.LocalAddr().(*net.UDPAddr)
	require.True(t, ok)

	if addr.IP.IsLoopback() {
		t.Skip("the only local address is loopback, so there is only one path to bond")
	}

	return addr.IP
}

// sender builds a test publisher that routes media over its gathered candidates.
func sender(t *testing.T, mode routing) (*webrtc.PeerConnection, *webrtc.TrackLocalStaticRTP, *paths) {
	t.Helper()

	lan := outboundIP(t)

	base, err := stdnet.NewNet()
	require.NoError(t, err)

	open := &paths{mode: mode}

	engine := webrtc.SettingEngine{}
	engine.SetNet(&sprayNet{Net: base, paths: open})
	engine.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	engine.SetIncludeLoopbackCandidate(true)
	engine.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	engine.SetIPFilter(func(ip net.IP) bool { return ip.IsLoopback() || ip.Equal(lan) })

	media := &webrtc.MediaEngine{}
	require.NoError(t, media.RegisterDefaultCodecs())

	// No interceptors, so nothing retransmits. A NACK responder would resend a
	// lost packet over the nominated pair, and this test would then pass with a
	// second path that carried nothing.
	api := webrtc.NewAPI(
		webrtc.WithMediaEngine(media),
		webrtc.WithInterceptorRegistry(&interceptor.Registry{}),
		webrtc.WithSettingEngine(engine),
	)

	peer, err := api.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = peer.Close() })

	// Static RTP rather than samples, because the test has to choose the
	// sequence numbers it later asserts on.
	track, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: clockRate}, "video", "bonded")
	require.NoError(t, err)

	_, err = peer.AddTrack(track)
	require.NoError(t, err)

	return peer, track, open
}

// fixture wires a spraying publisher into the ingest, attaches a viewer to the
// egress, and hands back a writer that returns the sequence number it used.
func fixture(t *testing.T, mode routing, options dynamicdelay.Options) (func() uint16, *arrivals, *paths) {
	t.Helper()

	cam := config.Ingest{
		Options:     options,
		ID:          "bonded",
		Label:       "Bonded",
		SenderKey:   config.SenderPrefix + "00000000000000000000000000000001",
		ReceiverKey: config.ReceiverPrefix + "00000000000000000000000000000002",
		Codecs:      []string{config.CodecH264},
		DelayMS:     config.DelayDefaultMS,
	}
	cfg := &config.Config{Ingests: []config.Ingest{cam}}

	engine, mux, err := ingest.NewSettingEngine(0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mux.Close() })

	log := logging.NewDefaultLoggerFactory().NewLogger("bonded")
	hub := relay.New()

	whip, err := ingest.NewServer(cfg, log, engine, hub, stats.New(), func(string) {})
	require.NoError(t, err)
	t.Cleanup(whip.Close)

	whep := egress.NewServer(cfg, log, whip.API(), hub)
	t.Cleanup(whep.Close)

	peer, track, open := sender(t, mode)

	answer, _, err := whip.Publish(cam.SenderKey, describe(t, peer))
	require.NoError(t, err)
	require.NoError(t, peer.SetRemoteDescription(
		webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}))

	// SPS then an IDR slice. Every packet is a keyframe so the playout buffer
	// never sits in catch up, which would drop packets this test would then
	// read as a merge failure.
	payload := []byte{
		0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x00, 0x1f,
		0x00, 0x00, 0x00, 0x01, 0x65, 0x88, 0x84, 0x00,
	}

	// One counter for the warm up and the measured window alike. A jump in the
	// sequence numbers would be a jump in the timestamps too, and the playout
	// buffer would hold the packets for the gap rather than the delay.
	var next uint16

	write := func() uint16 {
		next++

		_ = track.WriteRTP(&rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				Marker:         true,
				SequenceNumber: next,
				Timestamp:      uint32(next) * frameStep,
			},
			Payload: payload,
		})

		return next
	}

	seen := &arrivals{seen: map[uint16]int{}}
	attach(t, whep, &cam, seen, func() { _ = write() })

	return write, seen, open
}

// describe gathers a peer's candidates and returns the offer carrying them.
func describe(t *testing.T, peer *webrtc.PeerConnection) string {
	t.Helper()

	offer, err := peer.CreateOffer(nil)
	require.NoError(t, err)

	gathered := webrtc.GatheringCompletePromise(peer)
	require.NoError(t, peer.SetLocalDescription(offer))
	<-gathered

	return peer.LocalDescription().SDP
}

// attach connects a viewer and records every sequence number it receives. The
// publisher has to be sending before the relay has a track to subscribe to, so
// the warm up writer runs until the subscribe succeeds.
func attach(t *testing.T, whep *egress.Server, cam *config.Ingest, seen *arrivals, warmUp func()) {
	t.Helper()

	// Count the merge's output without downstream NACK/RTX adding repair
	// copies with the same original RTP sequence number.
	api := webrtc.NewAPI(webrtc.WithInterceptorRegistry(&interceptor.Registry{}))
	viewer, err := api.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = viewer.Close() })

	_, err = viewer.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
	require.NoError(t, err)

	viewer.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		for {
			pkt, _, readErr := track.ReadRTP()
			if readErr != nil {
				return
			}

			seen.add(pkt.SequenceNumber)
		}
	})

	offer := describe(t, viewer)

	var answer string

	require.Eventually(t, func() bool {
		warmUp()

		var subErr error
		answer, _, subErr = whep.Subscribe(cam.ReceiverKey, offer)

		return subErr == nil
	}, 30*time.Second, 100*time.Millisecond, "the publisher never reached the relay")

	require.NoError(t, viewer.SetRemoteDescription(
		webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}))
}

// run sends the measured window and waits for it to come out of the relay.
func run(t *testing.T, write func() uint16, seen *arrivals) (present, repeated int) {
	t.Helper()

	first := write()

	for range measured - 1 {
		time.Sleep(pace)
		write()
	}

	require.Eventually(t, func() bool {
		present, _ = seen.spanOf(first, measured)

		return present == measured
	}, 30*time.Second, 200*time.Millisecond, "media sent over the bonded paths never arrived whole")

	// Late copies are the failure this test exists for, so give them time to
	// show up rather than reading the counters the instant the last one lands.
	time.Sleep(2 * time.Second)

	return seen.spanOf(first, measured)
}

// The merge claim: media is accepted from any validated candidate, not only the
// selected pair. Half of this stream leaves by a socket ICE did not nominate,
// and the ingest still has to end up with all of it.
func TestMediaSplitAcrossPathsArrivesWhole(t *testing.T) {
	write, seen, open := fixture(t, splitPaths, dynamicdelay.Options{})

	require.GreaterOrEqual(t, open.count(), 2, "one socket cannot demonstrate a merge")

	present, _ := run(t, write, seen)

	assert.Equal(t, measured, present,
		"a packet sent over the candidate that was not nominated must still arrive")
}

// The deduplication claim: the SRTP replay detector discards the copies a
// spraying sender produces, so the relay forwards each packet once.
func TestTheSamePacketOnEveryPathArrivesOnce(t *testing.T) {
	write, seen, open := fixture(t, sprayPaths, dynamicdelay.Options{})

	require.GreaterOrEqual(t, open.count(), 2, "one socket cannot produce a duplicate")

	present, repeated := run(t, write, seen)

	require.GreaterOrEqual(t, open.duplicates(), measured,
		"the sockets have to have carried copies for there to be anything to drop")
	assert.Equal(t, measured, present, "spraying must not cost packets")
	assert.Zero(t, repeated,
		"every path carried the same packet, so the replay detector has to drop the copies")
}

func TestDynamicDelayPreservesBondedMedia(t *testing.T) {
	for _, scenario := range []struct {
		name string
		mode routing
		skew time.Duration
		rate int
	}{
		{name: "split", mode: splitPaths},
		{name: "duplicates", mode: sprayPaths},
		{name: "slow_path", mode: splitPaths, skew: 200 * time.Millisecond},
		{name: "late_path", mode: splitPaths, skew: 2300 * time.Millisecond},
		{name: "slow_catchup_late_path", mode: splitPaths, skew: 2300 * time.Millisecond, rate: 1},
		{name: "fast_catchup_late_path", mode: splitPaths, skew: 2300 * time.Millisecond, rate: 100},
		{name: "fast_catchup_duplicates", mode: sprayPaths, rate: 100},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			write, seen, open := fixture(t, scenario.mode, dynamicdelay.Options{
				Enabled: true, CatchUpMSPerSecond: scenario.rate,
			})
			require.GreaterOrEqual(t, open.count(), 2)
			open.mu.Lock()
			open.skew = scenario.skew
			open.mu.Unlock()
			present, repeated := run(t, write, seen)
			assert.Equal(t, measured, present)
			assert.Zero(t, repeated)
			if scenario.mode == sprayPaths {
				assert.GreaterOrEqual(t, open.duplicates(), measured)
			}
		})
	}
}
