// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package relay

import (
	"testing"
	"time"

	"github.com/pion/rtp"
)

// Measure queue operations without pacing or a growing producer backlog.
func BenchmarkBufferPushPop(b *testing.B) {
	buf := NewBuffer(0, testClock, "video/H264", nil)
	defer buf.Close()

	pkt := fixture(1)[0]

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		buf.Push(pkt)
		if _, ok := buf.Pop(); !ok {
			b.Fatal("buffer drained early")
		}
	}
}

// fixture builds n distinct packets carrying a keyframe payload.
func fixture(n int) []*rtp.Packet {
	payload := make([]byte, 1200)
	payload[0] = h264IDR

	packets := make([]*rtp.Packet, n)
	for i := range packets {
		packets[i] = &rtp.Packet{
			Header:  rtp.Header{SequenceNumber: uint16(i), Timestamp: uint32(i) * 3000}, //nolint:gosec // wrap is intended.
			Payload: payload,
		}
	}

	return packets
}

// The wait path, which is where a healthy stream spends its time: every packet
// is queued a target ahead of now, so the reader sleeps for each one. What is
// measured is the allocation, not the sleep.
func BenchmarkBufferPopWait(b *testing.B) {
	const spacing = 100 * time.Microsecond

	buf := NewBuffer(0, testClock, "video/H264", nil)
	defer buf.Close()

	// Timestamps run ahead of the wall clock, so each packet comes due one
	// spacing after the last and the reader waits on every one of them.
	packets := fixture(b.N)
	for i, pkt := range packets {
		pkt.Timestamp = uint32(i) * uint32(spacing*testClock/time.Second) //nolint:gosec // wrap is intended.
	}

	for _, pkt := range packets {
		buf.Push(pkt)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		if _, ok := buf.Pop(); !ok {
			b.Fatal("buffer drained early")
		}
	}
}

func BenchmarkBufferDepth(b *testing.B) {
	buf := NewBuffer(0, testClock, "video/H264", nil)
	defer buf.Close()
	now := time.Now()
	for i := range 1660 {
		buf.queue.push(buffered{playAt: now.Add(time.Duration(i) * time.Millisecond)})
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		buf.depthLocked()
	}
}

// The heap is the part that changed, so it is measured on its own. Scheduling a
// packet into the future is the buffer's job and would make this a benchmark of
// time.Until instead.
func BenchmarkHeapSteadyState(b *testing.B) {
	const depth = 1660

	queue := packetHeap{}
	now := time.Now()

	for i := range depth {
		queue.push(buffered{playAt: now.Add(time.Duration(i) * time.Millisecond)})
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := range b.N {
		queue.push(buffered{playAt: now.Add(time.Duration(depth+i) * time.Millisecond)})
		queue.pop()
	}
}

// Keyframe detection runs on every video packet and is the only place the relay
// looks inside a payload.
func BenchmarkKeyframe(b *testing.B) {
	for _, tc := range []struct {
		mime   string
		header []byte
	}{
		{"video/H264", []byte{h264FUA, h264FUStartBit | h264IDR}},
		{"video/H265", []byte{49 << 1, 0, 0x80 | 19}},
	} {
		b.Run(tc.mime, func(b *testing.B) {
			payload := make([]byte, 1200)
			copy(payload, tc.header)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if !isKeyframe(tc.mime, payload) {
					b.Fatal("expected a keyframe")
				}
			}
		})
	}
}
