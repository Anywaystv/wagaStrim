// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package ingest

import (
	"testing"
	"time"

	"github.com/Anywaystv/wagaStrim/internal/egress"
	"github.com/pion/logging"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testAACMime = "audio/MPEG4-GENERIC"

func aacPeer(t *testing.T) *webrtc.PeerConnection {
	t.Helper()

	media := &webrtc.MediaEngine{}
	require.NoError(t, media.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType: testAACMime, ClockRate: 48000, Channels: 2,
			SDPFmtpLine: "streamtype=5;profile-level-id=1;mode=AAC-hbr;config=1190;" +
				"sizelength=13;indexlength=3;indexdeltalength=3",
		},
		PayloadType: 112,
	}, webrtc.RTPCodecTypeAudio))
	peer, err := webrtc.NewAPI(webrtc.WithMediaEngine(media)).NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, peer.Close()) })

	return peer
}

func TestAACIsSelectedAutomaticallyFromTheOffer(t *testing.T) {
	srv, ing, _ := newTestServerWithRelay(t)
	publisher := aacPeer(t)
	_, err := publisher.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly})
	require.NoError(t, err)

	answer, _, err := srv.Publish(ing.SenderKey, offerFrom(t, publisher))
	require.NoError(t, err)
	assert.True(t, containsActiveAAC(answer))
	assert.Contains(t, answer, "MPEG4-GENERIC/48000/2")
}

func containsActiveAAC(answer string) bool {
	desc := webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}
	parsed, err := desc.Unmarshal()
	if err != nil {
		return false
	}

	for _, media := range parsed.MediaDescriptions {
		if media.MediaName.Media == "audio" && media.MediaName.Port.Value != 0 {
			return true
		}
	}

	return false
}

func TestAACPacketsReachCompatibleWHEPReceiver(t *testing.T) {
	srv, ing, hub := newTestServerWithRelay(t)
	publisher := aacPeer(t)
	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
		MimeType: testAACMime, ClockRate: 48000, Channels: 2,
	}, "audio", "aac-test")
	require.NoError(t, err)
	_, err = publisher.AddTrack(track)
	require.NoError(t, err)
	answer, _, err := srv.Publish(ing.SenderKey, offerFrom(t, publisher))
	require.NoError(t, err)
	require.NoError(t, publisher.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}))

	// The relay must carry the AU headers and data unchanged; it is not a decoder.
	payload := []byte{0, 16, 0, 24, 1, 2, 3}
	var sequence uint16
	write := func() {
		sequence++
		require.NoError(t, track.WriteRTP(&rtp.Packet{
			Header: rtp.Header{
				Version: 2, PayloadType: 112, SequenceNumber: sequence,
				Timestamp: uint32(sequence) * 1024, SSRC: 42, Marker: true,
			},
			Payload: payload,
		}))
	}
	require.Eventually(t, func() bool {
		write()
		_, subscribeErr := hub.Subscribe(ing.ID)

		return subscribeErr == nil
	}, 10*time.Second, 20*time.Millisecond)

	log := logging.NewDefaultLoggerFactory().NewLogger("aac-test")
	whep := egress.NewServer(srv.cfg, log, srv.API(), hub)
	t.Cleanup(whep.Close)
	viewer := aacPeer(t)
	_, err = viewer.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
	require.NoError(t, err)
	received := make(chan *rtp.Packet, 1)
	viewer.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		packet, _, readErr := remote.ReadRTP()
		if readErr == nil {
			received <- packet
		}
	})
	answer, _, err = whep.Subscribe(ing.ReceiverKey, offerFrom(t, viewer))
	require.NoError(t, err)
	require.NoError(t, viewer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}))

	var packet *rtp.Packet
	require.Eventually(t, func() bool {
		write()
		select {
		case packet = <-received:
			return true
		default:
			return false
		}
	}, 10*time.Second, 20*time.Millisecond)
	assert.Equal(t, payload, packet.Payload)
	assert.Equal(t, uint32(packet.SequenceNumber)*1024, packet.Timestamp)
}

func TestAACDoesNotSilentlyNegotiateAnOpusViewer(t *testing.T) {
	srv, ing, hub := newTestServerWithRelay(t)
	_, err := hub.Publish(ing.ID, webrtc.RTPCodecTypeAudio, webrtc.RTPCodecCapability{
		MimeType: testAACMime, ClockRate: 48000, Channels: 2,
	}, nil)
	require.NoError(t, err)
	log := logging.NewDefaultLoggerFactory().NewLogger("aac-test")
	whep := egress.NewServer(srv.cfg, log, srv.API(), hub)
	t.Cleanup(whep.Close)
	viewer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, viewer.Close()) })
	_, err = viewer.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
	require.NoError(t, err)
	_, _, err = whep.Subscribe(ing.ReceiverKey, offerFrom(t, viewer))
	require.Error(t, err, "an AAC track must not become a silent Opus preview")
}
