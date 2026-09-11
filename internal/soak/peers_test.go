// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package soak

import (
	"testing"
	"time"

	"github.com/Anywaystv/wagaStrim/internal/config"
	"github.com/Anywaystv/wagaStrim/internal/egress"
	"github.com/Anywaystv/wagaStrim/internal/ingest"
	"github.com/pion/webrtc/v4"
	pionmedia "github.com/pion/webrtc/v4/pkg/media"
	"github.com/stretchr/testify/require"
)

// keyframe is a minimal H.264 access unit: SPS then an IDR slice. Every frame
// is a keyframe so drift correction always has somewhere to resume, which keeps
// the soak measuring leaks rather than encoder behavior.
func keyframe() []byte {
	return []byte{
		0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x00, 0x1f,
		0x00, 0x00, 0x00, 0x01, 0x65, 0x88, 0x84, 0x00,
	}
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

// attachPublisher connects a synthetic phone and returns a frame writer.
func attachPublisher(t *testing.T, whip *ingest.Server, cam *config.Ingest) func() {
	t.Helper()

	peer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = peer.Close() })

	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000}, "video", "soak")
	require.NoError(t, err)

	_, err = peer.AddTrack(track)
	require.NoError(t, err)

	answer, _, err := whip.Publish(cam.SenderKey, gather(t, peer))
	require.NoError(t, err)
	require.NoError(t, peer.SetRemoteDescription(
		webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}))

	frame := keyframe()

	return func() {
		_ = track.WriteSample(pionmedia.Sample{Data: frame, Duration: 33 * time.Millisecond})
	}
}

// attachSubscriber waits for media to reach the relay, then attaches a viewer
// and keeps reading. A soak with nobody watching would miss a leak on the
// egress side, which is where the fan out lives.
func attachSubscriber(t *testing.T, whep *egress.Server, cam *config.Ingest, writeFrame func()) {
	t.Helper()

	viewer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = viewer.Close() })

	_, err = viewer.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
	require.NoError(t, err)

	viewer.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		for {
			if _, _, readErr := track.ReadRTP(); readErr != nil {
				return
			}
		}
	})

	// Gathered once. SetLocalDescription cannot be called twice on one peer, so
	// only the subscribe is retried while the publisher's media arrives.
	offer := gather(t, viewer)

	var answer string

	require.Eventually(t, func() bool {
		writeFrame()

		var subErr error
		answer, _, subErr = whep.Subscribe(cam.ReceiverKey, offer)

		return subErr == nil
	}, 30*time.Second, 250*time.Millisecond, "publisher never reached the relay")

	require.NoError(t, viewer.SetRemoteDescription(
		webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}))
}
