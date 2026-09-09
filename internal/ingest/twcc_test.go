// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package ingest

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"
)

func TestCustomBufferReturnsTWCCAndStopsOnReconnect(t *testing.T) {
	server, ing := newTestServer(t)
	for range 3 {
		mediaEngine := &webrtc.MediaEngine{}
		require.NoError(t, mediaEngine.RegisterDefaultCodecs())
		registry := &interceptor.Registry{}
		require.NoError(t, webrtc.RegisterDefaultInterceptors(mediaEngine, registry))
		require.NoError(t, webrtc.ConfigureTWCCHeaderExtensionSender(mediaEngine, registry))
		peer, err := webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine),
			webrtc.WithInterceptorRegistry(registry)).NewPeerConnection(webrtc.Configuration{})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, peer.Close()) })
		track, err := webrtc.NewTrackLocalStaticSample(
			webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000}, "video", "twcc")
		require.NoError(t, err)
		sender, err := peer.AddTrack(track)
		require.NoError(t, err)
		answer, resource, err := server.Publish(ing.SenderKey, offerFrom(t, peer))
		require.NoError(t, err)
		require.Contains(t, answer, "rtx/90000")
		require.Contains(t, answer, "transport-wide-cc")
		require.NoError(t, peer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}))
		var reports atomic.Uint32
		stopped := make(chan struct{})
		go func() {
			defer close(stopped)
			for {
				packets, _, readErr := sender.ReadRTCP()
				if readErr != nil {
					return
				}
				for _, packet := range packets {
					if _, ok := packet.(*rtcp.TransportLayerCC); ok {
						reports.Add(1)
					}
				}
			}
		}()
		frame := []byte{0, 0, 0, 1, 0x65, 0x88}
		require.Eventually(t, func() bool {
			_ = track.WriteSample(media(frame))

			return reports.Load() > 0
		}, 5*time.Second, 20*time.Millisecond, "TWCC feedback did not reach the publisher")
		require.NoError(t, server.Teardown(resource))
		require.NoError(t, peer.Close())
		select {
		case <-stopped:
		case <-time.After(time.Second):
			require.FailNow(t, "RTCP reader survived publisher shutdown")
		}
	}
}
