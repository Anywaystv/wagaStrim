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

func TestDynamicJumpSharesClockAndDiscardsPreKeyframeAudio(t *testing.T) {
	now := time.Now()
	var control dynamicdelay.Controller
	control.Configure(time.Second, dynamicdelay.Options{Enabled: true, MaximumMS: 2000, Jump: true}, now)
	video := NewBuffer(time.Second, testClock, "video/H264", func() {})
	audio := NewBuffer(time.Second, 48000, "audio/opus", nil)
	defer video.Close()
	defer audio.Close()
	for _, buf := range []*Buffer{video, audio} {
		buf.dynamic = &control
		buf.based = true
		buf.baseWall = now.Add(-3 * time.Second)
		buf.baseRTP = 0
	}
	video.Push(packet(1, 0, interFrame()...))
	audio.Push(packet(1, 0, 1))
	require.True(t, control.Step(now, 2*time.Second).Pending)
	video.Push(packet(2, 90000, idr()...))
	state := control.State()
	require.False(t, state.Pending)
	require.Len(t, video.queue, 1)
	audio.Push(packet(2, 0, 1))
	assert.Empty(t, audio.queue, "audio before the keyframe must not survive the jump")
	audio.Push(packet(3, 48000, 1))
	require.Len(t, audio.queue, 1)
	assert.Equal(t, video.baseWall, audio.baseWall)
	assert.Equal(t, video.queue[0].playAt, audio.queue[0].playAt)
	assert.Equal(t, state.Shift, audio.shift)
}

func TestDynamicCurveDoesNotMoveQueuedTimestampOrder(t *testing.T) {
	now := time.Now()
	var control dynamicdelay.Controller
	control.Configure(time.Second, dynamicdelay.Options{Enabled: true, MaximumMS: 10000}, now)
	buf := NewBuffer(time.Second, testClock, "video/H264", nil)
	defer buf.Close()
	buf.dynamic = &control
	for _, seq := range []uint16{3, 1, 2} {
		buf.Push(packet(seq, uint32(seq)*3000, idr()...))
	}
	original := buf.queue[0].playAt
	control.Step(now, 300*time.Millisecond)
	assert.Equal(t, 25*time.Millisecond, buf.dynamicOffsetLocked(now.Add(500*time.Millisecond)))
	assert.Equal(t, original, buf.queue[0].playAt)
	for _, want := range []uint16{1, 2, 3} {
		assert.Equal(t, want, buf.queue.pop().pkt.SequenceNumber)
	}
}

func TestMaximumDynamicDelayIsNotClockDrift(t *testing.T) {
	now := time.Now()
	var control dynamicdelay.Controller
	options := dynamicdelay.Options{Enabled: true, MaximumMS: 10000}
	control.Configure(10*time.Second, options, now)
	control.Configure(2*time.Second, options, now)
	buf := NewBuffer(2*time.Second, testClock, "video/H264", nil)
	defer buf.Close()
	buf.dynamic = &control
	buf.based = true
	buf.baseWall = now.Add(10 * time.Millisecond)
	buf.Push(packet(1, 0, idr()...))
	assert.False(t, buf.Correct())
	assert.Len(t, buf.queue, 1)
	assert.False(t, buf.catchUp)
}

func TestJumpDropsQueuedMediaEvenWhenTheClockShiftIsZero(t *testing.T) {
	now := time.Now()
	var control dynamicdelay.Controller
	control.Configure(time.Second, dynamicdelay.Options{Enabled: true, MaximumMS: 2000, Jump: true}, now)
	buf := NewBuffer(time.Second, testClock, "video/H264", nil)
	defer buf.Close()
	buf.dynamic = &control
	buf.Push(packet(1, 0, idr()...))
	require.True(t, control.Step(now, 2*time.Second).Pending)
	control.Jump(now, now)
	assert.Zero(t, control.State().Shift)
	buf.dynamicOffsetLocked(now)
	assert.Empty(t, buf.queue)
}

func TestLiveDynamicConfigurationCanRunAlongsidePackets(t *testing.T) {
	hub, _ := livePublisher(t)
	buf := NewBuffer(0, testClock, "video/H264", nil)
	defer buf.Close()
	hub.Track("cam", buf)
	hub.ConfigureDelay("cam", 0, dynamicdelay.Options{})
	assert.Nil(t, buf.dynamic, "disabled cameras keep the original wait path")
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 100 {
			buf.Push(packet(1, 0, idr()...))
			buf.Pop()
		}
	}()
	for index := range 100 {
		hub.ConfigureDelay("cam", 0, dynamicdelay.Options{Enabled: index%2 == 0, MaximumMS: 10000})
		buf.Correct()
	}
	<-done
}

func TestEnablingDynamicDelayIgnoresEarlierLatePackets(t *testing.T) {
	hub, _ := livePublisher(t)
	buf := NewBuffer(time.Second, testClock, "video/H264", nil)
	defer buf.Close()
	hub.Track("cam", buf)
	hub.ConfigureDelay("cam", time.Second, dynamicdelay.Options{})
	buf.based = true
	buf.baseWall = time.Now().Add(-4 * time.Second)
	buf.Push(packet(1, 0, idr()...))
	require.Positive(t, buf.peakLate)

	hub.ConfigureDelay("cam", time.Second, dynamicdelay.Options{Enabled: true, MaximumMS: 2000, Jump: true})
	buf.adjust(time.Now())
	state := buf.dynamic.State()
	require.NotNil(t, state)
	assert.False(t, state.Pending, "old lateness must not request a recovery jump")
	assert.Equal(t, time.Second, state.To)
}

func TestCorrectionAppliesJumpBeforeSamplingOrDisabling(t *testing.T) {
	for _, disable := range []bool{false, true} {
		t.Run(map[bool]string{false: "enabled", true: "disabled"}[disable], func(t *testing.T) {
			hub, _ := livePublisher(t)
			options := dynamicdelay.Options{Enabled: true, MaximumMS: 2000, Jump: true}
			hub.ConfigureDelay("cam", time.Second, options)
			video := NewBuffer(time.Second, testClock, "video/H264", func() {})
			audio := NewBuffer(time.Second, 48000, "audio/opus", nil)
			defer video.Close()
			defer audio.Close()
			now := time.Now()
			for _, buf := range []*Buffer{video, audio} {
				hub.Track("cam", buf)
				buf.based = true
				buf.baseWall = now.Add(-4 * time.Second)
				buf.Push(packet(1, 0, 1))
			}
			control := video.dynamic
			require.True(t, control.Step(now.Add(-2*time.Second), 3*time.Second).Pending)
			control.Jump(now, now.Add(-3*time.Second))
			video.dynamicOffsetLocked(now)
			if disable {
				options.Enabled = false
				hub.ConfigureDelay("cam", time.Second, options)
			}
			audio.adjust(now)
			assert.Equal(t, video.baseWall, audio.baseWall, "an idle track must apply the shared jump")
			assert.Empty(t, audio.queue)
			if disable {
				assert.Nil(t, control.State())
			} else {
				assert.False(t, control.State().Pending, "pre-jump lateness must not request another jump")
			}
		})
	}
}

func TestDelayedKeyframeCannotUndoAnEarlierJump(t *testing.T) {
	now := time.Now()
	var control dynamicdelay.Controller
	control.Configure(time.Second, dynamicdelay.Options{Enabled: true, MaximumMS: 2000, Jump: true}, now)
	buf := NewBuffer(time.Second, testClock, "video/H264", func() {})
	defer buf.Close()
	buf.dynamic = &control
	buf.based = true
	buf.baseWall = now.Add(-4 * time.Second)
	require.True(t, control.Step(now.Add(-3*time.Second), 3*time.Second).Pending)
	control.Jump(now.Add(-2*time.Second), buf.baseWall.Add(time.Second))
	buf.dynamicOffsetLocked(now)
	require.True(t, control.Step(now, 3*time.Second).Pending)
	before := control.State()
	buf.Push(packet(1, 0, idr()...))
	assert.Same(t, before, control.State(), "a keyframe older than the last jump cannot satisfy a new request")
	assert.Empty(t, buf.queue)
}

func TestDisablingAfterJumpStillRejectsSkippedMedia(t *testing.T) {
	now := time.Now()
	var control dynamicdelay.Controller
	options := dynamicdelay.Options{Enabled: true, MaximumMS: 2000, Jump: true}
	control.Configure(time.Second, options, now)
	buf := NewBuffer(time.Second, testClock, "video/H264", func() {})
	defer buf.Close()
	buf.dynamic = &control
	buf.based = true
	buf.baseWall = now.Add(-3 * time.Second)
	require.True(t, control.Step(now, 3*time.Second).Pending)
	buf.Push(packet(2, testClock, idr()...))
	require.Len(t, buf.queue, 1)
	options.Enabled = false
	control.Configure(time.Second, options, now.Add(time.Second))
	control.Step(now.Add(2*time.Second), 0)
	require.Nil(t, control.State())
	buf.Push(packet(1, 0, interFrame()...))
	assert.Len(t, buf.queue, 1, "disabling cannot admit packets from before the recovery point")
}

func TestFirstPacketAfterJumpDoesNotApplyHistoricalShift(t *testing.T) {
	now := time.Now()
	var control dynamicdelay.Controller
	control.Configure(time.Second, dynamicdelay.Options{Enabled: true, MaximumMS: 2000, Jump: true}, now)
	buf := NewBuffer(time.Second, 48000, "audio/opus", nil)
	defer buf.Close()
	buf.dynamic = &control
	require.True(t, control.Step(now, 3*time.Second).Pending)
	control.Jump(now, now.Add(-3*time.Second))
	buf.Push(packet(1, 0, 1))
	assert.False(t, buf.baseWall.After(time.Now()), "a track starting after recovery anchors to its first arrival")
	assert.Equal(t, control.State().Shift, buf.shift)
}

func TestResavingSameSettingsPreservesLatenessSample(t *testing.T) {
	hub, _ := livePublisher(t)
	options := dynamicdelay.Options{Enabled: true, MaximumMS: 2000, Jump: true}
	hub.ConfigureDelay("cam", time.Second, options)
	buf := NewBuffer(time.Second, testClock, "video/H264", nil)
	defer buf.Close()
	hub.Track("cam", buf)
	buf.based = true
	buf.baseWall = time.Now().Add(-4 * time.Second)
	buf.Push(packet(1, 0, idr()...))
	hub.ConfigureDelay("cam", time.Second, options)
	buf.adjust(time.Now())
	assert.True(t, buf.dynamic.State().Pending, "an unchanged API update must not erase recent lateness")
}
