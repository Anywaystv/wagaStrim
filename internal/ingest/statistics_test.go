// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package ingest

import (
	"testing"
	"time"

	"github.com/Anywaystv/wagaStrim/internal/dynamicdelay"
	"github.com/Anywaystv/wagaStrim/internal/relay"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	pionmedia "github.com/pion/webrtc/v4/pkg/media"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	statsFixedMode  = "fixed"
	statsManualMode = "manual"
)

func TestStatsIncludeCatchUpDropsWithoutAnotherVideoPacket(t *testing.T) {
	srv, ing, hub := newTestServerWithRelay(t)
	_, err := hub.Publish(ing.ID, webrtc.RTPCodecTypeVideo,
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000}, nil)
	require.NoError(t, err)
	hub.ConfigureDelay(ing.ID, 2*time.Second, dynamicdelay.Options{Enabled: true})
	buffer := relay.NewBuffer(2*time.Second, 90000, webrtc.MimeTypeH264, nil)
	t.Cleanup(buffer.Close)
	hub.Track(ing.ID, buffer)
	srv.stats.Publishing(ing.ID)
	buffer.Push(&rtp.Packet{Header: rtp.Header{SequenceNumber: 1}, Payload: []byte{0x65}})
	buffer.Push(&rtp.Packet{Header: rtp.Header{SequenceNumber: 2, Timestamp: 1800000}, Payload: []byte{0x65}})
	require.Zero(t, srv.stats.Of(ing.ID).Dropped)
	require.True(t, buffer.Correct())
	assert.Equal(t, uint64(2), srv.stats.Of(ing.ID).Dropped,
		"a correction must reach stats without another incoming video packet")
	srv.stats.ObserveBytes(ing.ID, 1000)
	assert.Equal(t, uint64(2), srv.stats.Of(ing.ID).Dropped, "audio must not clear catch-up drops")
	hub.Untrack(ing.ID, buffer)
	assert.Equal(t, uint64(2), srv.stats.Of(ing.ID).Dropped, "ended tracks keep their counters")
	assert.Nil(t, srv.stats.Of(ing.ID).Playout, "ended tracks have no current catch-up speed")
	srv.stats.Stopped(ing.ID)
	hub.Drop(ing.ID)
	assert.Equal(t, uint64(2), srv.stats.Of(ing.ID).Dropped)
	srv.stats.Publishing(ing.ID)
	assert.Zero(t, srv.stats.Of(ing.ID).Dropped, "replacement publishers start with new counters")
}

func TestStatsFollowLiveDelaySettingsAndSyncGroups(t *testing.T) {
	srv, ing, hub := newTestServerWithRelay(t)
	_, err := hub.Publish(ing.ID, webrtc.RTPCodecTypeVideo,
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000}, nil)
	require.NoError(t, err)
	buffer := relay.NewBuffer(2*time.Second, 90000, webrtc.MimeTypeH264, nil)
	t.Cleanup(buffer.Close)
	hub.Track(ing.ID, buffer)
	srv.stats.Publishing(ing.ID)
	for _, group := range []string{"", "rig", ""} {
		ing.SyncGroup = group
		ing.DelayMS = 3000
		ing.Options = dynamicdelay.Options{Enabled: true, CatchUpMSPerSecond: 200}
		hub.ConfigureDelay(ing.ID, 3*time.Second, srv.cfg.DelayOptions(ing.ID))
		playout := srv.stats.Report([]string{ing.ID})[ing.ID].Playout
		require.NotNil(t, playout)
		assert.Equal(t, group == "", playout.Enabled)
		assert.Equal(t, 3000, playout.DelayMS)
		assert.Equal(t, float64(3000), playout.CurrentDelayMS)
		assert.Zero(t, playout.CatchUpMSPerSecond)
	}
	srv.stats.Stopped(ing.ID)
	assert.Nil(t, srv.stats.Of(ing.ID).Playout)
}

func TestPublisherPacketLossReachesStats(t *testing.T) {
	for _, mode := range []string{statsFixedMode, "automatic"} {
		t.Run(mode, func(t *testing.T) {
			srv, ing := newTestServer(t)
			ing.Options = dynamicdelay.Options{
				Enabled: mode != statsFixedMode, CatchUpMSPerSecond: 100, AutoCatchUp: new(mode != statsManualMode),
			}
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
		})
	}
}

func TestAudioKeepsBitrateLiveAfterVideoPauses(t *testing.T) {
	for _, mode := range []string{statsFixedMode, "automatic", statsManualMode} {
		t.Run(mode, func(t *testing.T) {
			srv, ing := newTestServer(t)
			ing.Options = dynamicdelay.Options{
				Enabled: mode != statsFixedMode, CatchUpMSPerSecond: 100, AutoCatchUp: new(mode != statsManualMode),
			}
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
		})
	}
}
