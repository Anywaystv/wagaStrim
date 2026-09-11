// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package relay

import (
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testClock = 90000

func (b *Buffer) depth() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.depthLocked()
}

func videoCodec() webrtc.RTPCodecCapability {
	return webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: testClock}
}

func packet(seq uint16, timestamp uint32, payload ...byte) *rtp.Packet {
	return &rtp.Packet{
		Header:  rtp.Header{SequenceNumber: seq, Timestamp: timestamp},
		Payload: payload,
	}
}

// idr is an H.264 payload that starts a decodable picture.
func idr() []byte { return []byte{h264IDR} }

// interFrame is a payload that does not.
func interFrame() []byte { return []byte{0x01} }

func TestDropAllReleasesPacketReferences(t *testing.T) {
	buf := NewBuffer(time.Second, testClock, "video/H264", nil)
	defer buf.Close()
	buf.Push(packet(1, 0, idr()...))
	buf.Push(packet(2, 3000, interFrame()...))
	storage := buf.queue[:cap(buf.queue)]
	buf.dropAllLocked()
	require.Empty(t, buf.queue)
	require.Equal(t, len(storage), cap(buf.queue), "retain storage for reuse")
	require.EqualValues(t, 2, buf.dropped)
	for _, item := range storage {
		require.Nil(t, item.pkt, "discarded packets must be eligible for garbage collection")
	}
	buf.Push(packet(3, 6000, idr()...))
	require.Len(t, buf.queue, 1)
	require.EqualValues(t, 3, buf.queue[0].pkt.SequenceNumber)
}

func TestPacketIsHeldForTheTarget(t *testing.T) {
	buf := NewBuffer(150*time.Millisecond, testClock, "video/H264", nil)
	defer buf.Close()

	start := time.Now()
	buf.Push(packet(1, 0, idr()...))

	got, ok := buf.Pop()
	require.True(t, ok)
	assert.Equal(t, uint16(1), got.SequenceNumber)
	assert.GreaterOrEqual(t, time.Since(start), 140*time.Millisecond,
		"a packet released early defeats the point of the buffer")
}

func TestReorderingIsRepaired(t *testing.T) {
	buf := NewBuffer(100*time.Millisecond, testClock, "video/H264", nil)
	defer buf.Close()

	// Three frames, 33 ms apart, pushed out of order.
	buf.Push(packet(3, 6000, interFrame()...))
	buf.Push(packet(1, 0, idr()...))
	buf.Push(packet(2, 3000, interFrame()...))

	for _, want := range []uint16{1, 2, 3} {
		got, ok := buf.Pop()
		require.True(t, ok)
		assert.Equal(t, want, got.SequenceNumber, "packets must leave in timestamp order")
	}
}

// This is the behavior the whole product rests on. A packet that arrives late,
// because it was retransmitted, still lands in its correct slot and the output
// has no hole. Arrival-based scheduling could not do this.
func TestALatePacketStillMakesItsSlot(t *testing.T) {
	buf := NewBuffer(300*time.Millisecond, testClock, "video/H264", nil)
	defer buf.Close()

	buf.Push(packet(1, 0, idr()...))

	// Packet 2 belongs 33 ms after packet 1 but shows up 150 ms late.
	go func() {
		time.Sleep(150 * time.Millisecond)
		buf.Push(packet(2, 3000, interFrame()...))
	}()

	first, ok := buf.Pop()
	require.True(t, ok)
	require.Equal(t, uint16(1), first.SequenceNumber)

	second, ok := buf.Pop()
	require.True(t, ok)
	assert.Equal(t, uint16(2), second.SequenceNumber, "the retransmit must not be dropped")
}

func TestDriftCorrectionSkipsToAKeyframe(t *testing.T) {
	asked := make(chan struct{}, 1)
	buf := NewBuffer(50*time.Millisecond, testClock, "video/H264", func() {
		select {
		case asked <- struct{}{}:
		default:
		}
	})
	defer buf.Close()

	// Timestamps far in the future: the sender is running ahead of its own clock,
	// which is the drift no amount of waiting resolves.
	buf.Push(packet(1, 0, idr()...))

	for seq := uint16(2); seq < 12; seq++ {
		buf.Push(packet(seq, uint32(seq)*testClock, interFrame()...))
	}

	assert.Greater(t, buf.depth(), 50*time.Millisecond+hysteresis, "the buffer should be overfull")
	require.True(t, buf.Correct(), "drift past the hysteresis must trigger a correction")

	select {
	case <-asked:
	case <-time.After(time.Second):
		require.Fail(t, "a correction must ask the publisher for a keyframe")
	}

	// While catching up, inter frames are discarded and a keyframe resumes.
	buf.Push(packet(20, 20*testClock, interFrame()...))
	_, dropped := buf.Stats()
	assert.Positive(t, dropped, "inter frames must be dropped while catching up")

	buf.Push(packet(21, 21*testClock, idr()...))

	got, ok := buf.Pop()
	require.True(t, ok)
	assert.Equal(t, uint16(21), got.SequenceNumber, "playback must resume on a keyframe")
}

func TestADepthPastTenSecondsIsEmptiedRatherThanHeld(t *testing.T) {
	asks := make(chan struct{}, 4)
	buf := NewBuffer(50*time.Millisecond, testClock, "video/H264", func() {
		select {
		case asks <- struct{}{}:
		default:
		}
	})
	defer buf.Close()

	buf.Push(packet(1, 0, idr()...))

	// Twelve seconds of timestamps against a 50ms target. A publisher that never
	// answers the first keyframe request leaves the buffer here indefinitely,
	// which is the case this ceiling exists for.
	for seq := uint16(2); seq < 14; seq++ {
		buf.Push(packet(seq, uint32(seq)*12*testClock/12, interFrame()...))
	}

	buf.Push(packet(14, 12*testClock, interFrame()...))
	require.Greater(t, buf.depth(), resetCeiling, "the queue must be past the ceiling for this test to mean anything")

	require.True(t, buf.Correct(), "a queue past the ceiling must correct")
	assert.Zero(t, buf.depth(), "the queue must be emptied rather than held for a keyframe")

	select {
	case <-asks:
	case <-time.After(time.Second):
		require.Fail(t, "the reset must ask the publisher for a keyframe")
	}
}

func TestASkipWaitingOnAKeyframeAsksAgainEverySecond(t *testing.T) {
	asks := make(chan struct{}, 4)
	buf := NewBuffer(50*time.Millisecond, testClock, "video/H264", func() {
		select {
		case asks <- struct{}{}:
		default:
		}
	})
	defer buf.Close()

	buf.Push(packet(1, 0, idr()...))
	buf.Push(packet(2, 12*testClock, interFrame()...))
	require.True(t, buf.Correct(), "the first pass is the reset")
	<-asks

	// The skip is now in flight and its queue reads empty, because inter frames
	// are dropped while catching up. A publisher that ignored the first request
	// leaves the picture frozen here, so every later pass has to ask again.
	buf.Push(packet(3, 24*testClock, interFrame()...))
	require.Zero(t, buf.depth(), "inter frames are dropped while catching up")
	assert.False(t, buf.Correct(), "asking again is not a new correction")

	select {
	case <-asks:
	case <-time.After(time.Second):
		require.Fail(t, "a stalled catch-up must ask again")
	}
}

func TestPacketsDueTogetherKeepTheirArrivalOrder(t *testing.T) {
	buf := NewBuffer(10*time.Millisecond, testClock, "video/H264", nil)
	defer buf.Close()

	// One frame: ten packets, one timestamp, so all ten are due at the same
	// instant and only the arrival order separates them. Ordering by playAt alone
	// returned them shuffled, which reaches a receiver as gaps it asks to have
	// filled.
	for seq := uint16(1); seq <= 10; seq++ {
		buf.Push(packet(seq, 0, idr()...))
	}

	for want := uint16(1); want <= 10; want++ {
		got, ok := buf.Pop()
		require.True(t, ok)
		require.Equal(t, want, got.SequenceNumber, "a frame must leave in the order it arrived")
	}
}

func TestAFrameLeavesSpreadRatherThanAsOneBurst(t *testing.T) {
	buf := NewBuffer(20*time.Millisecond, testClock, "video/H264", nil)
	defer buf.Close()

	// Two frames a 33ms apart in sender time, the second one ten packets wide.
	// The first frame teaches the buffer the cadence; the second is the one that
	// has to leave spread across it rather than all at once.
	buf.Push(packet(1, 0, idr()...))

	for seq := uint16(2); seq <= 11; seq++ {
		buf.Push(packet(seq, testClock/30, interFrame()...))
	}

	first, ok := buf.Pop()
	require.True(t, ok)
	require.Equal(t, uint16(1), first.SequenceNumber)

	// The second frame is not due until its own playout time, so the clock starts
	// at its first packet: what is measured here is the spread of one group, not
	// the wait for the frame to arrive.
	second, ok := buf.Pop()
	require.True(t, ok)
	require.Equal(t, uint16(2), second.SequenceNumber)

	start := time.Now()

	for want := uint16(3); want <= 11; want++ {
		got, ok := buf.Pop()
		require.True(t, ok)
		require.Equal(t, want, got.SequenceNumber, "pacing must not reorder a frame")
	}

	spread := time.Since(start)
	assert.Greater(t, spread, 5*time.Millisecond, "a frame's packets must not leave as one burst")
	assert.Less(t, spread, 33*time.Millisecond, "and must all leave before the next frame is due")
}

func TestPacingReleasesAPacketThatIsAlreadyLate(t *testing.T) {
	buf := NewBuffer(0, testClock, "video/H264", nil)
	defer buf.Close()

	buf.Push(packet(1, 0, idr()...))

	// A group spacing wide enough to hold this packet for minutes. Nothing may
	// wait on it: the packet is past its slot, so the link is already behind.
	buf.mu.Lock()
	buf.group = buf.queue[0].playAt
	buf.groupGap = time.Hour
	buf.nextSlot = time.Now().Add(time.Hour)
	buf.mu.Unlock()

	done := make(chan struct{})

	go func() {
		defer close(done)

		_, ok := buf.Pop()
		assert.True(t, ok)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		require.Fail(t, "a late packet must not wait on the pacer")
	}
}

func TestTheCadenceComesFromThePlayoutTimes(t *testing.T) {
	buf := NewBuffer(50*time.Millisecond, testClock, "video/H264", nil)
	defer buf.Close()

	// Three frames, a thirtieth of a second apart by their timestamps.
	for frame := range 3 {
		buf.Push(packet(uint16(frame+1), uint32(frame)*testClock/30, idr()...)) //nolint:gosec // three.
	}

	for range 3 {
		_, ok := buf.Pop()
		require.True(t, ok)
	}

	buf.mu.Lock()
	gap := buf.frameGap
	buf.mu.Unlock()

	assert.InDelta(t, float64(testClock/30)/float64(testClock)*float64(time.Second), float64(gap),
		float64(3*time.Millisecond), "the frame interval must be read from the playout times")
}

func TestCorrectIsQuietWhenHealthy(t *testing.T) {
	buf := NewBuffer(2*time.Second, testClock, "video/H264", func() {
		require.Fail(t, "a healthy buffer must not request keyframes")
	})
	defer buf.Close()

	buf.Push(packet(1, 0, idr()...))
	assert.False(t, buf.Correct(), "depth at the target is not drift")
}

func TestTimestampWraparound(t *testing.T) {
	buf := NewBuffer(50*time.Millisecond, testClock, "video/H264", nil)
	defer buf.Close()

	// Base near the top of the uint32 range, next packet wraps past zero.
	buf.Push(packet(1, 0xFFFFFF00, idr()...))
	buf.Push(packet(2, 0x0000002C, interFrame()...))

	first, ok := buf.Pop()
	require.True(t, ok)
	require.Equal(t, uint16(1), first.SequenceNumber)

	second, ok := buf.Pop()
	require.True(t, ok)
	assert.Equal(t, uint16(2), second.SequenceNumber, "a wrapping timestamp must not sort backwards")
}

func TestCloseReleasesAReader(t *testing.T) {
	buf := NewBuffer(time.Hour, testClock, "video/H264", nil)

	done := make(chan bool, 1)

	go func() {
		_, ok := buf.Pop()
		done <- ok
	}()

	buf.Close()

	select {
	case ok := <-done:
		assert.False(t, ok, "a closed and drained buffer reports no more packets")
	case <-time.After(2 * time.Second):
		require.Fail(t, "Close must wake a blocked reader")
	}
}

func TestKeyframeDetection(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
		want    bool
	}{
		{"IDR", []byte{h264IDR}, true},
		{"SPS", []byte{h264SPS}, true},
		{"inter frame", []byte{0x01}, false},
		{"STAP-A carrying SPS", []byte{h264STAPA, 0x00, 0x01, h264SPS}, true},
		{"STAP-A without a keyframe", []byte{h264STAPA, 0x00, 0x01, 0x01}, false},
		{"FU-A starting an IDR", []byte{h264FUA, h264FUStartBit | h264IDR}, true},
		{"FU-A continuing an IDR", []byte{h264FUA, h264IDR}, false},
		{"empty", nil, false},
		{"truncated STAP-A", []byte{h264STAPA, 0x00, 0x09, 0x01}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isKeyframe("video/H264", tc.payload))
		})
	}
}

// An unrecognized codec must resume immediately rather than drop forever.
func TestUnknownCodecNeverStallsCorrection(t *testing.T) {
	assert.True(t, isKeyframe("video/VP9", []byte{0x00}))
}

func TestH265KeyframeDetection(t *testing.T) {
	// H.265 NAL type is bits 1..6 of the first byte, so the value is shifted.
	nal := func(t byte) byte { return t << 1 }

	cases := []struct {
		name    string
		payload []byte
		want    bool
	}{
		{"IDR_W_RADL", []byte{nal(19), 0x01}, true},
		{"CRA", []byte{nal(21), 0x01}, true},
		{"VPS", []byte{nal(32), 0x01}, true},
		{"PPS", []byte{nal(34), 0x01}, true},
		{"trailing picture", []byte{nal(1), 0x01}, false},
		{"aggregation with an IDR", []byte{nal(48), 0x01, 0x00, 0x02, nal(19), 0x01}, true},
		{"aggregation without one", []byte{nal(48), 0x01, 0x00, 0x02, nal(1), 0x01}, false},
		{"fragment starting an IDR", []byte{nal(49), 0x01, 0x80 | 19}, true},
		{"fragment continuing one", []byte{nal(49), 0x01, 19}, false},
		{"truncated aggregation", []byte{nal(48), 0x01, 0x00, 0x40, nal(19)}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isKeyframe("video/H265", tc.payload))
		})
	}
}

// AV1 carries no NAL types; the N bit of the aggregation header is the only
// keyframe signal available without decoding OBUs.
func TestAV1KeyframeDetection(t *testing.T) {
	assert.True(t, isKeyframe("video/AV1", []byte{0b0000_1000}), "N set starts a coded video sequence")
	assert.False(t, isKeyframe("video/AV1", []byte{0b0000_0000}), "N clear is mid sequence")
	assert.False(t, isKeyframe("video/AV1", nil))
}

// Resuming an H.265 stream on a trailing picture would show corruption. Before
// this phase every non-H.264 codec returned true and would have done exactly
// that.
func TestH265DoesNotResumeOnATrailingPicture(t *testing.T) {
	assert.False(t, isKeyframe("video/H265", []byte{1 << 1, 0x01}))
}

func TestLatePacketsAreCounted(t *testing.T) {
	buf := NewBuffer(20*time.Millisecond, testClock, "video/H264", nil)
	defer buf.Close()

	buf.Push(packet(1, 0, idr()...))

	// Belongs a full second before the base, so it is already past its slot.
	buf.Push(packet(2, ^uint32(0)-testClock+1, interFrame()...))

	late, _ := buf.Stats()
	assert.Equal(t, uint64(1), late, "a packet past its slot must be counted, not silently kept")
}

func TestRetargetReachesARunningBuffer(t *testing.T) {
	hub := New()

	track, err := hub.Publish("cam", webrtc.RTPCodecTypeVideo, videoCodec(), nil)
	require.NoError(t, err)
	require.NotNil(t, track)

	buf := NewBuffer(5*time.Second, testClock, "video/H264", nil)
	defer buf.Close()

	hub.Track("cam", buf)
	hub.Retarget("cam", 200*time.Millisecond)

	start := time.Now()
	buf.Push(packet(1, 0, idr()...))

	_, ok := buf.Pop()
	require.True(t, ok)
	assert.Less(t, time.Since(start), 2*time.Second,
		"a retarget must reach a buffer that is already running")
}

// Lowering the delay while media is queued used to put every packet arriving
// afterwards in front of everything already waiting, so the relay wrote roughly
// a target's worth of media out backwards.
func TestLoweringTheTargetKeepsTheQueueInOrder(t *testing.T) {
	buf := NewBuffer(time.Second, testClock, "video/H264", nil)
	defer buf.Close()

	buf.Push(packet(1, 0, idr()...))
	buf.Push(packet(2, 3000, interFrame()...))

	buf.SetTarget(20 * time.Millisecond)
	buf.Push(packet(3, 6000, interFrame()...))

	for _, want := range []uint16{1, 2, 3} {
		got, ok := buf.Pop()
		require.True(t, ok)
		assert.Equal(t, want, got.SequenceNumber, "a lowered target must not reorder the output")
	}
}

func TestRetargetIgnoresAnUnknownIngest(t *testing.T) {
	assert.NotPanics(t, func() { New().Retarget("gone", time.Second) })
}

// A link that reconnects repeatedly must not leave closed buffers behind.
func TestUntrackForgetsAClosedBuffer(t *testing.T) {
	hub := New()

	_, err := hub.Publish("cam", webrtc.RTPCodecTypeVideo, videoCodec(), nil)
	require.NoError(t, err)

	for range 20 {
		buf := NewBuffer(time.Second, testClock, "video/H264", nil)
		hub.Track("cam", buf)
		buf.Close()
		hub.Untrack("cam", buf)
	}

	hub.mu.RLock()
	held := len(hub.streams["cam"].buffers)
	hub.mu.RUnlock()

	assert.Zero(t, held, "twenty reconnects must not leave twenty dead buffers")
	storage := hub.streams["cam"].buffers
	for _, buf := range storage[:cap(storage)] {
		assert.Nil(t, buf, "untracked buffers must not remain referenced by the backing array")
	}
}

// Bonded paths arrive out of order by the skew between them, which the replay
// window has to tolerate. A packet a full second behind its neighbor still has
// to land in its slot rather than be treated as a stray.
func TestBondedSkewIsAbsorbed(t *testing.T) {
	buf := NewBuffer(2*time.Second, testClock, "video/H264", nil)
	defer buf.Close()

	// Fast path delivers frames 1 and 3; the slow path's frame 2 lands after both.
	buf.Push(packet(1, 0, idr()...))
	buf.Push(packet(3, 6000, interFrame()...))
	buf.Push(packet(2, 3000, interFrame()...))

	for _, want := range []uint16{1, 2, 3} {
		got, ok := buf.Pop()
		require.True(t, ok)
		assert.Equal(t, want, got.SequenceNumber,
			"a packet from the slower bonded path must still sort into place")
	}
}

// A compositor box runs the buffer far below the IRL floor. The old fixed
// margin was several times such a target, so drift there would never have been
// corrected at all.
func TestCorrectionMarginFollowsTheTarget(t *testing.T) {
	assert.Equal(t, hysteresis, correctionMargin(2*time.Second),
		"at the product floor the wide margin is what absorbs a recovery burst")
	assert.Equal(t, hysteresis, correctionMargin(8*time.Second),
		"a deeper buffer does not need a deeper margin than the burst it protects")
	assert.Equal(t, 300*time.Millisecond, correctionMargin(300*time.Millisecond),
		"a low target corrects at twice its depth rather than never")
	assert.Equal(t, minMargin, correctionMargin(20*time.Millisecond),
		"below the floor of the margin, jitter alone would trigger a skip")
}
