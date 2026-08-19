// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package ingest

import (
	"testing"
	"time"

	"github.com/MarcFryd/wagaStrim/internal/config"
	"github.com/pion/logging"
	"github.com/pion/webrtc/v4"
	pionmedia "github.com/pion/webrtc/v4/pkg/media"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestServer builds a server on an ephemeral media port with one ingest.
func newTestServer(t *testing.T) (*Server, *config.Ingest) {
	t.Helper()

	cfg := &config.Config{}
	ing, err := config.NewTestIngest("Chest cam")
	require.NoError(t, err)

	cfg.Ingests = append(cfg.Ingests, *ing)

	engine, mux, err := NewSettingEngine(0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mux.Close() })

	log := logging.NewDefaultLoggerFactory().NewLogger("test")

	srv, err := NewServer(cfg, log, engine)
	require.NoError(t, err)
	t.Cleanup(srv.Close)

	return srv, &cfg.Ingests[0]
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

	session, ok := srv.Session(resource)
	require.True(t, ok)

	frame := []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x00, 0x1f, 0x00, 0x00, 0x00, 0x01, 0x65, 0x88}

	require.Eventually(t, func() bool {
		_ = track.WriteSample(media(frame))

		return session.Bytes() > 0
	}, 15*time.Second, 50*time.Millisecond, "no media arrived on the ingest")

	require.NoError(t, srv.Teardown(resource))

	_, still := srv.Session(resource)
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

func TestSecondPublisherIsRejected(t *testing.T) {
	srv, ing := newTestServer(t)

	first, _ := publisher(t)
	_, _, err := srv.Publish(ing.SenderKey, offerFrom(t, first))
	require.NoError(t, err)

	second, _ := publisher(t)
	_, _, err = srv.Publish(ing.SenderKey, offerFrom(t, second))
	assert.ErrorIs(t, err, ErrAlreadyLive)
}

// media wraps a frame in the sample shape the track writer expects.
func media(frame []byte) pionmedia.Sample {
	return pionmedia.Sample{Data: frame, Duration: 33 * time.Millisecond}
}
