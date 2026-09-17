// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package relay

import (
	"testing"
	"time"

	"github.com/Anywaystv/wagaStrim/internal/dynamicdelay"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func clockBuffers(t *testing.T, hub *Relay, base time.Time, mime string) (*Buffer, *Buffer) {
	t.Helper()
	video := NewBuffer(2*time.Second, testClock, mime, func() {})
	audio := NewBuffer(2*time.Second, 48000, "audio/opus", nil)
	for _, buf := range []*Buffer{video, audio} {
		t.Cleanup(buf.Close)
		hub.Track("cam", buf)
		buf.based, buf.baseWall = true, base
	}

	return video, audio
}

func TestClockRecoveryPreservesSyncAfterUnevenDelivery(t *testing.T) {
	for _, codec := range []struct {
		mime string
		key  []byte
	}{
		{"video/H264", idr()},
		{"video/H265", []byte{19 << 1, 1}},
		{"video/AV1", []byte{8}},
	} {
		for _, enabled := range []bool{false, true} {
			name := codec.mime + map[bool]string{false: "/fixed", true: "/dynamic"}[enabled]
			t.Run(name, func(t *testing.T) {
				hub, _ := livePublisher(t)
				hub.ConfigureDelay("cam", 2*time.Second, dynamicdelay.Options{Enabled: enabled})
				base := time.Now().Add(-time.Second)
				video, audio := clockBuffers(t, hub, base, codec.mime)
				audio.baseWall = base.Add(20 * time.Millisecond)
				// Both RTP clocks jump three seconds; audio arrives 600 ms behind.
				video.Push(packet(1, 4*testClock, codec.key...))
				audio.Push(packet(1, 163200, 1))
				require.True(t, video.Correct())
				assert.False(t, audio.Correct(), "the second track must share the first correction")
				assert.Empty(t, video.queue)
				video.Push(packet(2, 4*testClock, 0))
				assert.Empty(t, video.queue, "video must still wait for a keyframe")
				video.Push(packet(3, 4*testClock, codec.key...))
				audio.Push(packet(2, 163200, 1))
				assert.InDelta(t, 20, float64(audio.playoutOf(192000).Sub(video.playoutOf(4*testClock)))/
					float64(time.Millisecond), 0.01)
				// Once delivery recovers, equal capture timestamps remain aligned.
				audio.Push(packet(3, 192000, 1))
				clocks := hub.Playback("cam").Clocks
				require.Len(t, clocks, 2)
				audioAtVideo := clocks[1].ReferenceMS +
					(4000 - float64(clocks[1].Timestamp)*1000/48000)
				assert.InDelta(t, 20, audioAtVideo-clocks[0].ReferenceMS, 0.01)
				assert.InDelta(t, 2000, float64(video.depth())/float64(time.Millisecond), 50)
			})
		}
	}
}

func TestClockRecoveryLeavesHealthyTrackAlone(t *testing.T) {
	for _, videoJumps := range []bool{false, true} {
		t.Run(map[bool]string{false: "audio", true: "video"}[videoJumps], func(t *testing.T) {
			hub, _ := livePublisher(t)
			base := time.Now()
			video, audio := clockBuffers(t, hub, base, "video/H264")
			jumped, healthy := audio, video
			if videoJumps {
				jumped, healthy = video, audio
			}
			jumped.Push(packet(1, 3*jumped.clockRate, idr()...))
			healthy.Push(packet(1, 0, idr()...))
			require.True(t, jumped.Correct())
			assert.Equal(t, base, healthy.baseWall)
			assert.Len(t, healthy.queue, 1)
			assert.False(t, healthy.catchUp)
			jumped.Push(packet(2, 3*jumped.clockRate, idr()...))
			assert.InDelta(t, 0, float64(jumped.playoutOf(3*jumped.clockRate).Sub(healthy.playoutOf(0)))/
				float64(time.Millisecond), 10)
		})
	}
}

func TestDelayedSiblingReusesRecentClockCorrection(t *testing.T) {
	hub, _ := livePublisher(t)
	video, audio := clockBuffers(t, hub, time.Now(), "video/H264")
	video.Push(packet(1, 3*testClock, idr()...))
	audio.Push(packet(1, 9600, 1))
	require.True(t, video.Correct())
	video.Push(packet(2, 3*testClock, idr()...))
	// Audio's 2.8-second delivery lag drains only partway before the next tick.
	audio.Push(packet(2, 100800, 1))
	require.True(t, audio.Correct())
	audio.Push(packet(3, 144000, 1))
	assert.Equal(t, video.baseWall, audio.baseWall)
	assert.InDelta(t, 2000, float64(audio.depth())/float64(time.Millisecond), 50)
	assert.False(t, video.Correct())

	// Another jump on the same track must start a new correction.
	previous := video.baseWall
	video.Push(packet(3, 495000, idr()...))
	require.True(t, video.Correct())
	assert.InDelta(t, 2500, float64(previous.Sub(video.baseWall))/float64(time.Millisecond), 50)
	assert.NotEqual(t, video.recoveryAt, audio.recoveryAt)

	// An unrelated later audio jump must not reuse expired video history.
	hub.streams["cam"].recoveryAt = time.Now().Add(-clockRecoveryWindow)
	audio.Push(packet(4, 288000, 1))
	require.True(t, audio.Correct())
	audio.Push(packet(5, 288000, 1))
	assert.InDelta(t, 2000, float64(audio.depth())/float64(time.Millisecond), 50)
}

func TestSenderReportsAlignQueuedAudioAfterTimestampGap(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "fixed", true: "dynamic"}[enabled], func(t *testing.T) {
			hub, _ := livePublisher(t)
			hub.ConfigureDelay("cam", 2*time.Second, dynamicdelay.Options{Enabled: enabled})
			base := time.Now().Add(-7 * time.Second)
			video, audio := clockBuffers(t, hub, base, "video/H264")
			// Audio's packet counter missed seven seconds, but its sender report
			// identifies the same capture instant as the resumed video.
			video.Push(packet(1, 7*testClock, idr()...))
			audio.Push(packet(1, 0, 1))
			ntp := uint64(4_000_000_000) << 32
			video.SenderReport(7*testClock, ntp)
			assert.False(t, video.Correct())
			assert.Equal(t, base, audio.baseWall, "wait for both sender clocks")
			audio.SenderReport(0, ntp)
			assert.False(t, video.Correct(), "align timing without discarding queued media")
			require.Len(t, audio.queue, 1)
			require.Len(t, video.queue, 1)
			assert.Equal(t, video.queue[0].playAt, audio.queue[0].playAt)
			assert.Equal(t, video.playoutOf(7*testClock), audio.playoutOf(0))
			assert.False(t, video.catchUp)
			assert.False(t, audio.catchUp)
			assert.Zero(t, audio.peakLate, "the old clock's lateness must not keep growing the buffer")
		})
	}
}

func TestSenderClockRecoveryKeepsTheSharedTimeline(t *testing.T) {
	for _, audioFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "video first", true: "audio first"}[audioFirst], func(t *testing.T) {
			hub, _ := livePublisher(t)
			video := NewBuffer(2*time.Second, testClock, "video/H264", func() {})
			audio := NewBuffer(2*time.Second, 48000, "audio/opus", nil)
			defer video.Close()
			defer audio.Close()
			buffers := []*Buffer{video, audio}
			if audioFirst {
				buffers = []*Buffer{audio, video}
			}
			base := time.Now()
			ntp := uint64(4_000_000_000) << 32
			for _, buf := range buffers {
				hub.Track("cam", buf)
				buf.based, buf.baseWall = true, base
				buf.SenderReport(0, ntp)
			}
			// Only video has delivered the jump yet. Complete sender reports
			// let recovery preserve the offset without guessing from arrivals.
			video.Push(packet(1, 3*testClock, idr()...))
			audio.Push(packet(1, 0, 1))
			require.True(t, video.Correct())
			assert.Equal(t, video.baseWall, audio.baseWall)
			video.Push(packet(2, 3*testClock, idr()...))
			audio.Push(packet(2, 3*48000, 1))
			assert.Equal(t, video.queue[0].playAt, audio.queue[0].playAt)

			// A newer report accounts for slow audio clock drift, including
			// already queued packets, without throwing away either track.
			videoBase := video.baseWall
			audio.SenderReport(3*48000+28800, ntp+(3<<32))
			audio.Push(packet(3, 3*48000+28800, 1))
			assert.False(t, video.Correct())
			assert.Equal(t, videoBase, video.baseWall, "audio drift must not move the shared reference")
			assert.InDelta(t, 0, float64(video.playoutOf(3*testClock).Sub(audio.playoutOf(3*48000+28800)))/
				float64(time.Millisecond), 0.01)
			assert.Len(t, audio.queue, 2)
		})
	}
}

func TestSenderTimestampResetDoesNotRebaseHealthyVideo(t *testing.T) {
	hub, _ := livePublisher(t)
	base := time.Now()
	video, audio := clockBuffers(t, hub, base, "video/H264")
	ntp := uint64(4_000_000_000) << 32
	for _, buf := range []*Buffer{video, audio} {
		buf.SenderReport(7*buf.clockRate, ntp)
		buf.Push(packet(1, 0, idr()...))
	}
	assert.False(t, video.Correct())
	// New audio starts its counter seven seconds earlier. Queued audio still
	// uses the old counter and must not be interpreted with the new mapping.
	audio.SenderReport(48000, ntp+(1<<32))
	assert.False(t, video.Correct())
	assert.Empty(t, audio.queue)
	assert.Equal(t, base, video.baseWall)
	assert.Len(t, video.queue, 1)
	audio.Push(packet(2, 0, 1))
	assert.Equal(t, video.playoutOf(7*testClock), audio.playoutOf(0))
}
