// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package ingest

import (
	"testing"
	"time"

	"github.com/MarcFryd/wagaStrim/internal/config"
	"github.com/MarcFryd/wagaStrim/internal/relay"
	"github.com/MarcFryd/wagaStrim/internal/stats"
	"github.com/pion/logging"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	pionmedia "github.com/pion/webrtc/v4/pkg/media"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestServer builds a server on an ephemeral media port with one ingest.
func newTestServer(t *testing.T) (*Server, *config.Ingest) {
	t.Helper()

	srv, ing, _ := newTestServerWithRelay(t)

	return srv, ing
}

// newTestServerWithRelay also hands back the relay so egress tests can attach.
func newTestServerWithRelay(t *testing.T) (*Server, *config.Ingest, *relay.Relay) {
	t.Helper()

	cfg := &config.Config{Ingests: []config.Ingest{testIngest("Chest cam")}}

	engine, mux, err := NewSettingEngine(0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mux.Close() })

	log := logging.NewDefaultLoggerFactory().NewLogger("test")

	hub := relay.New()

	srv, err := NewServer(cfg, log, engine, hub, stats.New(), func(string) {})
	require.NoError(t, err)
	t.Cleanup(srv.Close)

	return srv, &cfg.Ingests[0], hub
}

// publisher is a synthetic Moblin: it offers one sendonly H.264 track.
func publisher(t *testing.T) (*webrtc.PeerConnection, *webrtc.TrackLocalStaticSample) {
	t.Helper()

	peer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = peer.Close() })

	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000}, "video", "synthetic")
	require.NoError(t, err)

	_, err = peer.AddTrack(track)
	require.NoError(t, err)

	return peer, track
}

func offerFrom(t *testing.T, peer *webrtc.PeerConnection) string {
	t.Helper()

	offer, err := peer.CreateOffer(nil)
	require.NoError(t, err)

	gathered := webrtc.GatheringCompletePromise(peer)
	require.NoError(t, peer.SetLocalDescription(offer))
	<-gathered

	return peer.LocalDescription().SDP
}

func TestPublisherMediaReachesTheIngest(t *testing.T) {
	srv, ing := newTestServer(t)
	peer, track := publisher(t)

	answer, resource, err := srv.Publish(ing.SenderKey, offerFrom(t, peer))
	require.NoError(t, err)
	require.NotEmpty(t, resource)

	require.NoError(t, peer.SetRemoteDescription(
		webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}))

	connected := make(chan struct{})
	peer.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateConnected {
			close(connected)
		}
	})

	select {
	case <-connected:
	case <-time.After(15 * time.Second):
		require.Fail(t, "publisher never connected")
	}

	session, ok := srv.session(resource)
	require.True(t, ok)

	frame := []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x00, 0x1f, 0x00, 0x00, 0x00, 0x01, 0x65, 0x88}

	require.Eventually(t, func() bool {
		_ = track.WriteSample(media(frame))

		return session.Bytes() > 0
	}, 15*time.Second, 50*time.Millisecond, "no media arrived on the ingest")

	assert.Equal(t, "H.264", srv.stats.Of(ing.ID).Codec,
		"the negotiated codec must be discovered from the track, not assumed from the toggles")

	require.NoError(t, srv.Teardown(resource))

	_, still := srv.session(resource)
	assert.False(t, still, "teardown must forget the session")
}

func TestReceiverKeyAtWhipIsRefused(t *testing.T) {
	srv, ing := newTestServer(t)
	peer, _ := publisher(t)

	_, _, err := srv.Publish(ing.ReceiverKey, offerFrom(t, peer))
	assert.ErrorIs(t, err, ErrWrongRole, "the OBS link must not publish")
}

func TestUnknownKeyIsIndistinguishable(t *testing.T) {
	srv, _ := newTestServer(t)
	peer, _ := publisher(t)

	_, _, err := srv.Publish("s_deadbeefdeadbeefdeadbeefdeadbeef", offerFrom(t, peer))
	assert.ErrorIs(t, err, ErrUnknownKey)
}

func TestRecvonlyOfferIsRefused(t *testing.T) {
	srv, ing := newTestServer(t)

	peer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = peer.Close() })

	_, err = peer.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
	require.NoError(t, err)

	_, _, err = srv.Publish(ing.SenderKey, offerFrom(t, peer))
	assert.ErrorIs(t, err, ErrNotSending, "a WHEP client pointed here must be told so")
}

// A phone that loses its link is retrying long before ICE calls the old session
// failed. Refusing it for that whole window is the reconnect a streamer notices.
func TestAPublisherReplacesOneThatNeverConnected(t *testing.T) {
	srv, ing := newTestServer(t)

	first, _ := publisher(t)
	_, firstResource, err := srv.Publish(ing.SenderKey, offerFrom(t, first))
	require.NoError(t, err)

	second, _ := publisher(t)
	_, secondResource, err := srv.Publish(ing.SenderKey, offerFrom(t, second))
	require.NoError(t, err, "a session that is not carrying media must not lock the camera")

	_, still := srv.session(firstResource)
	assert.False(t, still, "the replaced session must be gone, not merely shadowed")
	assert.NotEqual(t, firstResource, secondResource)
}

// The other half: two cameras pointed at one ingest is a mistake, and the one
// actually on air keeps it.
func TestAConnectedPublisherIsNotDisplaced(t *testing.T) {
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

	second, _ := publisher(t)
	_, _, err = srv.Publish(ing.SenderKey, offerFrom(t, second))
	assert.ErrorIs(t, err, ErrAlreadyLive)
}

// media wraps a frame in the sample shape the track writer expects.
func media(frame []byte) pionmedia.Sample {
	return pionmedia.Sample{Data: frame, Duration: 33 * time.Millisecond}
}

func TestDescribePairNamesTheRoute(t *testing.T) {
	pair := func(typ webrtc.ICECandidateType, addr string) *webrtc.ICECandidatePair {
		return &webrtc.ICECandidatePair{
			Local:  &webrtc.ICECandidate{Address: "192.168.1.5"},
			Remote: &webrtc.ICECandidate{Typ: typ, Address: addr, Protocol: webrtc.ICEProtocolUDP},
		}
	}

	assert.Contains(t, describePair(pair(webrtc.ICECandidateTypeRelay, "1.2.3.4")), "relayed")
	assert.Contains(t, describePair(pair(webrtc.ICECandidateTypeSrflx, "1.2.3.4")), "through NAT")
	assert.Contains(t, describePair(pair(webrtc.ICECandidateTypeHost, "192.168.1.9")), "local network")
	assert.Contains(t, describePair(pair(webrtc.ICECandidateTypeHost, "1.2.3.4")), "direct")
	assert.Contains(t, describePair(pair(webrtc.ICECandidateTypeHost, "1.2.3.4")), "192.168.1.5")
}

// The path callback hangs off SCTP().Transport().ICETransport(), which has to
// exist on a media-only connection with no data channel. If pion ever stops
// creating it there, path reporting silently disappears rather than failing.
func TestPathIsReportedOnAMediaOnlyConnection(t *testing.T) {
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

	assert.NotEmpty(t, srv.stats.Of(ing.ID).Path,
		"a connected publisher must report the path carrying it")
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

func TestForwardedPacketsCarryNoPublisherExtensions(t *testing.T) {
	// Header extension ids are negotiated per session, so an id forwarded from
	// the publisher names a different extension on the subscriber's side. What is
	// asserted here is the clearing itself, since the hop it protects needs two
	// peer connections to observe.
	pkt := &rtp.Packet{
		Header:  rtp.Header{Version: 2, ExtensionProfile: 0xBEDE},
		Payload: []byte{0x01, 0x02},
	}

	require.NoError(t, pkt.SetExtension(1, []byte{0xAA, 0xBB, 0xCC}))
	require.True(t, pkt.Extension)
	require.NotEmpty(t, pkt.Extensions)

	stripExtensions(pkt)

	assert.False(t, pkt.Extension, "a forwarded packet must not claim an extension")
	assert.Empty(t, pkt.Extensions, "and must carry none")
	assert.Equal(t, []byte{0x01, 0x02}, pkt.Payload, "clearing must not touch the media")
}
