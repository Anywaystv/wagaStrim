// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package relay

import (
	"testing"
	"time"

	"github.com/Anywaystv/wagaStrim/internal/dynamicdelay"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlaybackClocksKeepTrackOffsetAndRecentTimestamp(t *testing.T) {
	hub := New()
	assert.Empty(t, hub.Playback("missing").Clocks)
	_, err := hub.Publish("camera", webrtc.RTPCodecTypeVideo,
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000}, nil)
	require.NoError(t, err)
	base := time.Now()
	video := NewBuffer(time.Second, 90000, webrtc.MimeTypeH264, nil)
	audio := NewBuffer(time.Second, 48000, webrtc.MimeTypeOpus, nil)
	hub.Track("camera", video)
	hub.Track("camera", audio)
	assert.Empty(t, hub.Playback("camera").Clocks)
	video.based, video.baseRTP, video.baseWall = true, 0xfffffff0, base
	audio.based, audio.baseRTP, audio.baseWall = true, 12345, base.Add(20*time.Millisecond)
	video.Push(&rtp.Packet{Header: rtp.Header{Timestamp: 8984}})
	audio.Push(&rtp.Packet{Header: rtp.Header{Timestamp: 17145}})
	clocks := hub.Playback("camera").Clocks
	require.Len(t, clocks, 2)
	assert.Equal(t, uint32(8984), clocks[0].Timestamp)
	assert.Equal(t, "video", clocks[0].Kind)
	assert.InDelta(t, 20, clocks[1].ReferenceMS-clocks[0].ReferenceMS, 0.01)
	video.baseWall = video.baseWall.Add(time.Second)
	video.shift = time.Second
	assert.Equal(t, clocks, hub.Playback("camera").Clocks, "partially applied jumps must preserve the track offset")
	audio.baseWall = audio.baseWall.Add(time.Second)
	audio.shift = time.Second
	assert.Equal(t, clocks, hub.Playback("camera").Clocks)
	video.SetTarget(5 * time.Second)
	assert.Equal(t, clocks, hub.Playback("camera").Clocks)
	options := dynamicdelay.Options{Enabled: true}
	hub.ConfigureDelay("camera", 9*time.Second, options)
	hub.ConfigureDelay("camera", 2*time.Second, options)
	assert.Equal(t, float64(9000), hub.Playback("camera").CurrentDelayMS)
	hub.Drop("camera")
	assert.Empty(t, hub.Playback("camera").Clocks)
}

func TestSenderClocksSurviveLongStreamsAndDelayedReports(t *testing.T) {
	hub, _ := livePublisher(t)
	video := NewBuffer(time.Second, 90000, "video/H264", nil)
	audio := NewBuffer(time.Second, 48000, "audio/opus", nil)
	defer video.Close()
	defer audio.Close()
	for _, buf := range []*Buffer{video, audio} {
		hub.Track("cam", buf)
		buf.Push(packet(1, 0xfffffff0, 1))
	}
	audio.baseWall = audio.baseWall.Add(600 * time.Millisecond)
	fallback := hub.Playback("cam").Clocks
	ntp := uint64(4_000_000_000) << 32
	video.SenderReport(0xfffffff0, ntp)
	assert.Equal(t, fallback, hub.Playback("cam").Clocks, "one report must not mix clock epochs")

	// Simulate 24 hours of reports and RTP wraps without a real-time soak.
	for seconds := uint64(0); seconds <= 24*60*60; seconds += 60 {
		videoStamp := uint32((0xfffffff0 + seconds*90000) & 0xffffffff)
		audioStamp := uint32((0xfffffff0 + seconds*48000 + seconds/60*48) & 0xffffffff)
		video.SenderReport(videoStamp, ntp+(seconds<<32))
		audio.SenderReport(audioStamp, ntp+(seconds<<32))
		for idx, buf := range []*Buffer{video, audio} {
			buf.dropAllLocked()
			buf.Push(packet(2, []uint32{videoStamp, audioStamp}[idx], 1))
		}
		clocks := hub.Playback("cam").Clocks
		require.Len(t, clocks, 2)
		assert.Equal(t, clocks[0].ReferenceMS, clocks[1].ReferenceMS,
			"sender reports must account for accumulated audio clock drift")
		// Delayed, duplicate and zero reports must not undo a newer mapping.
		audio.SenderReport(audioStamp+48000, 0)
		audio.SenderReport(audioStamp+48000, ntp+(seconds<<32))
		audio.SenderReport(audioStamp+48000, ntp+(seconds<<32)-(1<<32))
		assert.Equal(t, clocks, hub.Playback("cam").Clocks)
	}
}

func TestLongStreamPlayoutKeepsRecentRTPAnchor(t *testing.T) {
	for _, rate := range []uint32{48000, 90000} {
		buf := NewBuffer(time.Second, rate, "", nil)
		buf.Push(packet(1, 0xfffffff0, 1))
		start := buf.queue[0].playAt
		for seconds := uint64(60); seconds <= 24*60*60; seconds += 60 {
			stamp := uint32((0xfffffff0 + seconds*uint64(rate)) & 0xffffffff)
			buf.Push(packet(2, stamp, 1))
			assert.Equal(t, start.Add(time.Duration(seconds)*time.Second), buf.playoutOf(stamp))
			assert.Equal(t, start, buf.queue[0].playAt, "rolling the anchor must not move queued packets")
		}
		buf.Close()
	}
}
