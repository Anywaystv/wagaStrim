// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package relay

import (
	"testing"
	"time"

	"github.com/pion/rtp"
)

// The relay touches every packet, so what matters is per-packet cost and
// allocation rate, not throughput. At 8 Mbps that is roughly 830 packets a
// second per camera.
func BenchmarkBufferPushPop(b *testing.B) {
	buf := NewBuffer(0, testClock, "video/H264", nil)
	defer buf.Close()

	// Packets are built up front. Allocating one per iteration would measure the
	// benchmark rather than the buffer.
	packets := fixture(b.N)

	go func() {
		for range b.N {
			buf.Pop()
		}
	}()

	b.ReportAllocs()
	b.ResetTimer()

	for i := range b.N {
		buf.Push(packets[i])
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
func BenchmarkKeyframeH264(b *testing.B) {
	payload := make([]byte, 1200)
	payload[0] = h264FUA
	payload[1] = h264FUStartBit | h264IDR

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		if !isKeyframe("video/H264", payload) {
			b.Fatal("expected a keyframe")
		}
	}
}

func BenchmarkKeyframeH265(b *testing.B) {
	payload := make([]byte, 1200)
	payload[0] = 49 << 1
	payload[2] = 0x80 | 19

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		if !isKeyframe("video/H265", payload) {
			b.Fatal("expected a keyframe")
		}
	}
}
