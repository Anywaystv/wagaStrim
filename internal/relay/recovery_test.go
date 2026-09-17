// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package relay

import (
	"testing"
	"time"

	"github.com/Anywaystv/wagaStrim/internal/dynamicdelay"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestForwardResetPreservesBuffering(t *testing.T) {
	for _, seconds := range []uint32{5, 10, 3600} {
		for mode, enabled := range map[string]bool{"fixed": false, "dynamic": true} {
			for _, order := range []string{"before", "after", "staggered"} {
				name := (time.Duration(seconds) * time.Second).String() + "/" + mode + "/" + order
				t.Run(name, func(t *testing.T) {
					forwardResetPreservesBuffering(t, seconds, enabled, order)
				})
			}
		}
	}
}

func forwardResetPreservesBuffering(t *testing.T, seconds uint32, enabled bool, order string) {
	t.Helper()
	hub, _ := livePublisher(t)
	hub.ConfigureDelay("cam", 2*time.Second, dynamicdelay.Options{Enabled: enabled})
	video, audio := clockBuffers(t, hub, time.Now().Add(-time.Second), webrtc.MimeTypeH264)
	buffers := []*Buffer{video, audio}
	ntp := uint64(4_000_000_000) << 32
	for _, buf := range buffers {
		buf.Push(packet(1, 0, idr()...))
		buf.SenderReport(0, ntp)
	}
	require.False(t, video.Correct())
	if order == "before" {
		for _, buf := range buffers {
			buf.SenderReport(seconds*buf.clockRate, ntp+(1<<32))
		}
	}
	for _, buf := range buffers {
		buf.Push(packet(2, seconds*buf.clockRate, idr()...))
	}
	video.Correct()
	for _, buf := range buffers {
		buf.SenderReport(seconds*buf.clockRate, ntp+(1<<32))
		if order == "staggered" {
			video.Correct()
		}
	}
	video.Correct()
	for _, buf := range buffers {
		stamp := seconds*buf.clockRate + buf.clockRate/50
		buf.Push(packet(3, stamp, idr()...))
		require.Len(t, buf.queue, 1)
		assert.InDelta(t, 2020, float64(time.Until(buf.playoutOf(stamp)))/float64(time.Millisecond), 100)
		buf.SenderReport((seconds+1)*buf.clockRate, ntp+(2<<32))
	}
	assert.False(t, video.Correct())
	assert.WithinDuration(t, video.queue[0].playAt, audio.queue[0].playAt, 50*time.Millisecond)
	assert.Greater(t, time.Until(video.queue[0].playAt), time.Second)
}

func TestFeedEndsAfterUntrackWithFutureMedia(t *testing.T) {
	hub, _ := livePublisher(t)
	buf := NewBuffer(2*time.Second, testClock, webrtc.MimeTypeH264, nil)
	hub.Track("cam", buf)
	tracks, err := hub.Subscribe("cam")
	require.NoError(t, err)
	buf.Push(packet(1, 0, idr()...))
	buf.Push(packet(2, 3600*testClock, idr()...))
	done := make(chan struct{})
	go func() {
		Feed(tracks[0], buf, func(error) {})
		close(done)
	}()
	defer func() {
		buf.mu.Lock()
		buf.dropAllLocked()
		buf.ready.Signal()
		buf.mu.Unlock()
		<-done
	}()
	hub.Untrack("cam", buf)
	buf.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		assert.Fail(t, "closed feed retained future-dated media")
	}
}

func TestCloseStillDrainsScheduledMedia(t *testing.T) {
	buf := NewBuffer(20*time.Millisecond, testClock, webrtc.MimeTypeH264, nil)
	buf.Push(packet(1, 0, idr()...))
	buf.Close()
	pkt, ok := buf.Pop()
	require.True(t, ok)
	assert.Equal(t, uint16(1), pkt.SequenceNumber)
	_, ok = buf.Pop()
	assert.False(t, ok)
}

func anchoredBuffers(t *testing.T) (*Buffer, *Buffer, uint64) {
	t.Helper()
	hub, _ := livePublisher(t)
	video, audio := clockBuffers(t, hub, time.Now().Add(-time.Second), "video/H264")
	ntp := uint64(4_000_000_000) << 32
	for _, b := range []*Buffer{video, audio} {
		b.Push(packet(1, 0, idr()...))
		b.SenderReport(0, ntp)
	}
	require.False(t, video.Correct())

	return video, audio, ntp
}

func TestSenderCorrectionSurvivesUnrelatedResets(t *testing.T) {
	video, audio, ntp := anchoredBuffers(t)
	video.Push(packet(2, 5*video.clockRate, idr()...))
	require.True(t, video.Correct())
	shared := video.stream.senderBaseWall
	audio.SenderReport(5*audio.clockRate, ntp+(1<<32))
	video.Correct()
	assert.Equal(t, shared, video.stream.senderBaseWall, "sibling's independent reset cannot revoke video correction")
	video.SenderReport(5*video.clockRate, ntp+(5<<32))
	video.Correct()
	assert.Equal(t, shared, video.stream.senderBaseWall)
	assert.Zero(t, video.correctionShift)
	video.SenderReport(10*video.clockRate, ntp+(6<<32))
	video.Correct()
	assert.Equal(t, shared, video.stream.senderBaseWall, "later report-first reset cannot revoke confirmed correction")
}

func TestSenderCorrectionAccumulatesBeforeReport(t *testing.T) {
	video, audio, ntp := anchoredBuffers(t)
	before := video.stream.senderBaseWall
	video.Push(packet(2, 5*video.clockRate, idr()...))
	require.True(t, video.Correct())
	video.Push(packet(3, 10*video.clockRate, idr()...))
	require.True(t, video.Correct())
	video.SenderReport(10*video.clockRate, ntp+(1<<32))
	audio.SenderReport(audio.clockRate, ntp+(1<<32))
	video.Correct()
	assert.WithinDuration(t, before, video.stream.senderBaseWall, 20*time.Millisecond)
}

func TestSenderCorrectionsStayWithTheirTracks(t *testing.T) {
	video, audio, ntp := anchoredBuffers(t)
	video.Push(packet(2, 5*video.clockRate, idr()...))
	require.True(t, video.Correct())
	afterVideo := video.stream.senderBaseWall
	audio.Push(packet(2, 10*audio.clockRate, idr()...))
	require.True(t, video.Correct())
	audio.SenderReport(10*audio.clockRate, ntp+(1<<32))
	video.Correct()
	assert.WithinDuration(t, afterVideo, video.stream.senderBaseWall, 20*time.Millisecond)
	video.SenderReport(5*video.clockRate, ntp+(5<<32))
	video.Correct()
	assert.WithinDuration(t, afterVideo, video.stream.senderBaseWall, 20*time.Millisecond)
}
