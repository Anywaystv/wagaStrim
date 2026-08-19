// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package relay

import (
	"math"
	"sync"
	"time"

	"github.com/pion/rtp"
)

// Buffer holds media for a fixed interval before releasing it, so a packet that
// arrives late or by retransmission can still make its slot.
//
// Release is scheduled from the RTP timestamp, never from arrival. Arrival-based
// release would only add a constant delay: a gap in arrivals would reappear as a
// gap in output. Timestamp-based release means a packet delayed by half a second
// still lands in the right place, and the output has no hole at all.
type Buffer struct {
	mu     sync.Mutex
	ready  *sync.Cond
	queue  packetHeap
	closed bool

	target    time.Duration
	clockRate uint32
	mime      string

	// Mapping from the sender's RTP clock to ours, fixed on the first packet.
	based    bool
	baseRTP  uint32
	baseWall time.Time

	// catchUp drops video until the next keyframe, which is how a buffer that
	// has grown past its target is drained. See Depth.
	catchUp  bool
	keyframe func()

	late    uint64
	dropped uint64
}

// hysteresis is deliberately wide. A link recovering from a dropout delivers a
// burst, and a narrow margin would read that as drift and skip a keyframe at the
// exact moment the picture came back.
const hysteresis = 2 * time.Second

// NewBuffer builds a buffer for one track.
func NewBuffer(target time.Duration, clockRate uint32, mime string, keyframe func()) *Buffer {
	buf := &Buffer{
		target:    target,
		clockRate: clockRate,
		mime:      mime,
		keyframe:  keyframe,
	}
	buf.ready = sync.NewCond(&buf.mu)

	return buf
}

// Push queues a packet. It never blocks the reader.
func (b *Buffer) Push(pkt *rtp.Packet) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return
	}

	if !b.based {
		b.baseRTP = pkt.Timestamp
		b.baseWall = time.Now()
		b.based = true
	}

	playAt := b.playoutOf(pkt.Timestamp)

	// Past its slot already. Queue it anyway: a late packet still beats a hole,
	// and the count is what tells the UI the target is too low for this link.
	if playAt.Before(time.Now()) {
		b.late++
	}

	if b.catchUp && !isKeyframe(b.mime, pkt.Payload) {
		b.dropped++

		return
	}

	if b.catchUp {
		// Resuming on a keyframe is only half the correction. The clock mapping
		// still carries the drift that caused it, so this keyframe would play out
		// as late as everything it replaced. Rebase onto now, which is what
		// actually removes the accumulated offset.
		b.catchUp = false
		b.dropAllLocked()
		b.baseRTP = pkt.Timestamp
		b.baseWall = time.Now()
		playAt = b.playoutOf(pkt.Timestamp)
	}

	b.queue.push(buffered{pkt: pkt, playAt: playAt})
	b.ready.Signal()
}

// playoutOf maps a sender timestamp onto our clock.
func (b *Buffer) playoutOf(timestamp uint32) time.Time {
	elapsed := rtpDelta(timestamp, b.baseRTP)
	offset := time.Duration(elapsed) * time.Second / time.Duration(b.clockRate)

	return b.baseWall.Add(offset).Add(b.target)
}

// rtpDelta returns how far ahead of base a timestamp is, in clock ticks. RTP
// timestamps are uint32 and wrap, so the subtraction is done in uint32 and the
// upper half of the range is read as a negative delta. Without this a stream
// crossing the wrap point would appear to jump four billion ticks into the past.
func rtpDelta(timestamp, base uint32) int64 {
	diff := timestamp - base
	if diff > math.MaxInt32 {
		return -int64(^diff + 1)
	}

	return int64(diff)
}

// Pop blocks until the next packet is due and returns it. It returns false once
// the buffer is closed and drained.
func (b *Buffer) Pop() (*rtp.Packet, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for {
		if b.closed && len(b.queue) == 0 {
			return nil, false
		}

		if len(b.queue) == 0 {
			b.ready.Wait()

			continue
		}

		wait := time.Until(b.queue[0].playAt)
		if wait <= 0 {
			return b.queue.pop().pkt, true
		}

		b.waitUntilLocked(wait)
	}
}

// waitUntilLocked sleeps without holding the lock, waking early if a packet that
// is due sooner arrives or the buffer closes.
func (b *Buffer) waitUntilLocked(wait time.Duration) {
	timer := time.AfterFunc(wait, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.ready.Signal()
	})
	defer timer.Stop()

	b.ready.Wait()
}

// depth is how far ahead of now the newest queued packet is scheduled. It sits
// at roughly the target while the link is healthy. Growing past target plus
// hysteresis means media is arriving faster than its timestamps say it should,
// which no amount of waiting will resolve.
func (b *Buffer) depth() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.depthLocked()
}

func (b *Buffer) depthLocked() time.Duration {
	newest := time.Duration(0)

	for _, item := range b.queue {
		if until := time.Until(item.playAt); until > newest {
			newest = until
		}
	}

	return newest
}

// Correct drains an overfull buffer by skipping to the next keyframe rather than
// playing faster. A pass-through relay cannot resample video, and speeding audio
// pitches it, so one clean skip beats sustained distortion. Reports whether a
// correction started.
func (b *Buffer) Correct() bool {
	b.mu.Lock()

	if b.catchUp || b.depthLocked() <= b.target+hysteresis {
		b.mu.Unlock()

		return false
	}

	b.catchUp = true
	ask := b.keyframe
	b.mu.Unlock()

	if ask != nil {
		ask()
	}

	return true
}

func (b *Buffer) dropAllLocked() {
	b.dropped += uint64(len(b.queue)) //nolint:gosec // a queue length is never negative.
	b.queue = b.queue[:0]
}

// Stats reports packets that missed their slot and packets discarded while
// catching up. A rising late count means the target is too low for this link.
func (b *Buffer) Stats() (late, dropped uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.late, b.dropped
}

// SetTarget changes the playout target of a running buffer. Clamping is the
// caller's job; config does it on every path that can set one.
func (b *Buffer) SetTarget(target time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.target = target
	b.ready.Signal()
}

// Close releases any blocked reader once the queue is drained.
func (b *Buffer) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.closed = true
	b.ready.Broadcast()
}

// buffered is one packet and the moment it is due.
type buffered struct {
	pkt    *rtp.Packet
	playAt time.Time
}

// packetHeap is a binary min-heap ordered by playout time, which also repairs
// reordering: a packet arriving out of order sorts back into place.
//
// container/heap would do this, but its Push and Pop take and return `any`, so
// every packet boxed a buffered value onto the heap. That was two allocations
// per packet on a path that carries several hundred a second per camera.
type packetHeap []buffered

func (h *packetHeap) push(item buffered) {
	*h = append(*h, item)

	child := len(*h) - 1
	for child > 0 {
		parent := (child - 1) / 2
		if !(*h)[child].playAt.Before((*h)[parent].playAt) {
			break
		}

		(*h)[child], (*h)[parent] = (*h)[parent], (*h)[child]
		child = parent
	}
}

func (h *packetHeap) pop() buffered {
	old := *h
	top := old[0]
	last := len(old) - 1

	old[0] = old[last]
	old[last] = buffered{}
	*h = old[:last]

	h.sink(0)

	return top
}

// sink restores the heap property downward from one index.
func (h *packetHeap) sink(parent int) {
	size := len(*h)

	for {
		left, smallest := 2*parent+1, parent

		if left < size && (*h)[left].playAt.Before((*h)[smallest].playAt) {
			smallest = left
		}

		if right := left + 1; right < size && (*h)[right].playAt.Before((*h)[smallest].playAt) {
			smallest = right
		}

		if smallest == parent {
			return
		}

		(*h)[parent], (*h)[smallest] = (*h)[smallest], (*h)[parent]
		parent = smallest
	}
}
