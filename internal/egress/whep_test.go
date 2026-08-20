// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package egress_test

import (
	"sync"
	"testing"
	"time"

	"github.com/MarcFryd/wagaStrim/internal/config"
	"github.com/MarcFryd/wagaStrim/internal/egress"
	"github.com/MarcFryd/wagaStrim/internal/ingest"
	"github.com/MarcFryd/wagaStrim/internal/relay"
	"github.com/MarcFryd/wagaStrim/internal/stats"
	"github.com/pion/logging"
	"github.com/pion/webrtc/v4"
	pionmedia "github.com/pion/webrtc/v4/pkg/media"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pipeline builds the whole path: publisher, relay, subscriber.
func pipeline(t *testing.T) (*ingest.Server, *egress.Server, *config.Ingest) {
	t.Helper()

	cfg := &config.Config{Ingests: []config.Ingest{testIngest("Chest cam")}}

	engine, mux, err := ingest.NewSettingEngine(0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mux.Close() })

	log := logging.NewDefaultLoggerFactory().NewLogger("test")
	hub := relay.New()

	// Wired as main wires it.
	var whep *egress.Server

	whip, err := ingest.NewServer(cfg, log, engine, hub, stats.New(),
		func(id string) { whep.CloseIngest(id) })
	require.NoError(t, err)
	t.Cleanup(whip.Close)

	whep = egress.NewServer(cfg, log, whip.API(), hub)
	t.Cleanup(whep.Close)

	return whip, whep, &cfg.Ingests[0]
}

func gather(t *testing.T, peer *webrtc.PeerConnection) string {
	t.Helper()

	offer, err := peer.CreateOffer(nil)
	require.NoError(t, err)

	done := webrtc.GatheringCompletePromise(peer)
	require.NoError(t, peer.SetLocalDescription(offer))
	<-done

	return peer.LocalDescription().SDP
}

// startPublisher attaches a synthetic Moblin and returns a frame writer.
func startPublisher(t *testing.T, whip *ingest.Server, ing *config.Ingest) func() {
	t.Helper()

	peer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = peer.Close() })

	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000}, "video", "synthetic")
	require.NoError(t, err)

	_, err = peer.AddTrack(track)
	require.NoError(t, err)

	answer, _, err := whip.Publish(ing.SenderKey, gather(t, peer))
	require.NoError(t, err)
	require.NoError(t, peer.SetRemoteDescription(
		webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}))

	frame := []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x00, 0x1f, 0x00, 0x00, 0x00, 0x01, 0x65, 0x88}

	return func() {
		_ = track.WriteSample(pionmedia.Sample{Data: frame, Duration: 33 * time.Millisecond})
	}
}

// waitLive blocks until a subscriber can attach, which is the condition every
// test here depends on. Media only reaches the relay after ICE completes and
// the first packet lands.
func waitLive(t *testing.T, whep *egress.Server, ing *config.Ingest, writeFrame func()) {
	t.Helper()

	require.Eventually(t, func() bool {
		writeFrame()

		_, resource, err := whep.Subscribe(ing.ReceiverKey, recvOffer(t))
		if err != nil {
			return false
		}

		_ = whep.Teardown(resource)

		return true
	}, 20*time.Second, 200*time.Millisecond, "publisher never reached the relay")
}

func TestMediaReachesASubscriber(t *testing.T) {
	whip, whep, ing := pipeline(t)
	waitLive(t, whep, ing, startPublisher(t, whip, ing))

	_, resource, err := whep.Subscribe(ing.ReceiverKey, recvOffer(t))
	require.NoError(t, err)
	assert.NotEmpty(t, resource)
	require.NoError(t, whep.Teardown(resource))
}

func TestSubscriberReceivesPackets(t *testing.T) {
	whip, whep, ing := pipeline(t)
	writeFrame := startPublisher(t, whip, ing)

	viewer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = viewer.Close() })

	_, err = viewer.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
	require.NoError(t, err)

	got := make(chan struct{})
	viewer.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if _, _, readErr := track.ReadRTP(); readErr == nil {
			close(got)
		}
	})

	waitLive(t, whep, ing, writeFrame)

	answer, _, err := whep.Subscribe(ing.ReceiverKey, gather(t, viewer))
	require.NoError(t, err)

	require.NoError(t, viewer.SetRemoteDescription(
		webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}))

	stop := make(chan struct{})
	defer close(stop)

	go func() {
		tick := time.NewTicker(33 * time.Millisecond)
		defer tick.Stop()

		for {
			select {
			case <-tick.C:
				writeFrame()
			case <-stop:
				return
			}
		}
	}()

	select {
	case <-got:
	case <-time.After(20 * time.Second):
		require.Fail(t, "no RTP reached the subscriber")
	}
}

func recvOffer(t *testing.T) string {
	t.Helper()

	peer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = peer.Close() })

	_, err = peer.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
	require.NoError(t, err)

	return gather(t, peer)
}

// Without this a subscriber sits connected and frozen for good once its
// publisher reconnects.
func TestPublisherEndingDisconnectsItsSubscribers(t *testing.T) {
	whip, whep, ing := pipeline(t)
	writeFrame := startPublisher(t, whip, ing)

	viewer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = viewer.Close() })

	_, err = viewer.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
	require.NoError(t, err)

	gone := make(chan struct{})
	closeOnce := sync.OnceFunc(func() { close(gone) })

	viewer.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		switch state {
		case webrtc.PeerConnectionStateDisconnected,
			webrtc.PeerConnectionStateFailed,
			webrtc.PeerConnectionStateClosed:
			closeOnce()
		default:
		}
	})

	waitLive(t, whep, ing, writeFrame)

	answer, _, err := whep.Subscribe(ing.ReceiverKey, gather(t, viewer))
	require.NoError(t, err)
	require.NoError(t, viewer.SetRemoteDescription(
		webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}))

	require.Eventually(t, func() bool {
		return viewer.ConnectionState() == webrtc.PeerConnectionStateConnected
	}, 20*time.Second, 100*time.Millisecond, "the subscriber never connected")

	whip.CloseIngest(ing.ID)

	select {
	case <-gone:
	case <-time.After(30 * time.Second):
		require.Fail(t, "the subscriber was left on a track nothing will ever write to again")
	}
}

func TestSenderKeyAtWhepIsRefused(t *testing.T) {
	_, whep, ing := pipeline(t)

	_, _, err := whep.Subscribe(ing.SenderKey, recvOffer(t))
	assert.ErrorIs(t, err, egress.ErrWrongRole, "the Moblin link must not subscribe")
}

func TestSubscribingToAnIdleIngestSaysSo(t *testing.T) {
	_, whep, ing := pipeline(t)

	_, _, err := whep.Subscribe(ing.ReceiverKey, recvOffer(t))
	assert.ErrorIs(t, err, egress.ErrOffline, "an idle camera is not a negotiation failure")
}

func TestSendonlyOfferIsRefused(t *testing.T) {
	whip, whep, ing := pipeline(t)
	waitLive(t, whep, ing, startPublisher(t, whip, ing))

	peer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = peer.Close() })

	_, err = peer.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly})
	require.NoError(t, err)

	_, _, err = whep.Subscribe(ing.ReceiverKey, gather(t, peer))
	assert.ErrorIs(t, err, egress.ErrNotReceiving)
}

func TestPlayerPageCarriesNoConfiguration(t *testing.T) {
	page, err := egress.PlayerPage()
	require.NoError(t, err)

	assert.Contains(t, string(page), "/player/", "the page derives its endpoint from its own path")
	assert.NotContains(t, string(page), "s_", "no sender key may appear in a page served to viewers")
}

// testIngest builds a camera with fixed keys. Real randomness buys a test
// nothing and a readable key makes a failure easier to place.
func testIngest(label string) config.Ingest {
	return config.Ingest{
		ID:          "cam-" + label,
		Label:       label,
		SenderKey:   config.SenderPrefix + "00000000000000000000000000000001",
		ReceiverKey: config.ReceiverPrefix + "00000000000000000000000000000002",
		Codecs:      []string{config.CodecH264},
		DelayMS:     config.DelayDefaultMS,
	}
}
