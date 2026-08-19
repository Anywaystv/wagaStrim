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

	assert.Greater(t, buf.Depth(), 50*time.Millisecond+hysteresis, "the buffer should be overfull")
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
	assert.True(t, isKeyframe("video/AV1", []byte{0x00}))
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

func TestRetargetIgnoresAnUnknownIngest(t *testing.T) {
	assert.NotPanics(t, func() { New().Retarget("gone", time.Second) })
}
