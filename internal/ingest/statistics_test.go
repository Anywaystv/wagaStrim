// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package ingest

import (
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	pionmedia "github.com/pion/webrtc/v4/pkg/media"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublisherPacketLossReachesStats(t *testing.T) {
	srv, ing := newTestServer(t)
	peer, track := publisher(t)
	answer, resource, err := srv.Publish(ing.SenderKey, offerFrom(t, peer))
	require.NoError(t, err)
	require.NoError(t, peer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}))
	session, ok := srv.session(resource)
	require.True(t, ok)
	frame := media([]byte{0, 0, 0, 1, 0x65, 0x88})
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		assert.NoError(collect, track.WriteSample(frame))
		assert.Positive(collect, session.Bytes())
	}, 10*time.Second, 20*time.Millisecond)

	// Skip a sequence number that the sender cannot retransmit from its cache.
	missing := frame
	missing.PrevDroppedPackets = 1
	require.NoError(t, track.WriteSample(missing))
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		assert.NoError(collect, track.WriteSample(frame))
		assert.Positive(collect, srv.stats.Of(ing.ID).Lost)
	}, 5*time.Second, 20*time.Millisecond, "the dashboard must see loss from the real inbound stream")
	assert.Equal(t, uint64(1), srv.stats.Of(ing.ID).Lost)
}

func TestAudioKeepsBitrateLiveAfterVideoPauses(t *testing.T) {
	srv, ing := newTestServer(t)
	peer, video := publisher(t)
	audio, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{
		MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2,
	}, "audio", "synthetic")
	require.NoError(t, err)
	_, err = peer.AddTrack(audio)
	require.NoError(t, err)
	answer, _, err := srv.Publish(ing.SenderKey, offerFrom(t, peer))
	require.NoError(t, err)
	require.NoError(t, peer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}))
	frame := media([]byte{0, 0, 0, 1, 0x65, 0x88})
	sound := pionmedia.Sample{Data: make([]byte, 160), Duration: 20 * time.Millisecond}
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		assert.NoError(collect, video.WriteSample(frame))
		assert.NoError(collect, audio.WriteSample(sound))
		assert.Positive(collect, srv.stats.Of(ing.ID).Bitrate)
	}, 10*time.Second, 20*time.Millisecond)

	// Outlast the five-second bitrate window with audio alone.
	require.Never(t, func() bool {
		return !assert.NoError(t, audio.WriteSample(sound)) || srv.stats.Of(ing.ID).Bitrate == 0
	}, 6*time.Second, 20*time.Millisecond)
	assert.Equal(t, "H.264", srv.stats.Of(ing.ID).Codec)
}
