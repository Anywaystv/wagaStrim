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
	timer  *time.Timer
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

	// Pacing. Every packet of a frame carries one timestamp, so without this the
	// whole frame becomes due at the same instant and leaves as one burst -- a
	// measured 96 packets back to back, then nothing for 33ms. A receiver on a
	// shared radio queues that clump, drains it late, and plays the result frame
	// by frame. paceRate is bytes a second, learned from what arrives rather than
	// configured, and zero until a rate has been measured: a buffer that has not
	// seen a second of media yet paces nothing.
	// arrived counts pushes, which is what orders packets that are due together.
	arrived uint64

	bytesIn  uint64
	paceAt   time.Time
	paceRate float64
	nextSlot time.Time

	late    uint64
	dropped uint64
}

// hysteresis is deliberately wide. A link recovering from a dropout delivers a
// burst, and a narrow margin would read that as drift and skip a keyframe at the
// exact moment the picture came back.
//
// minMargin keeps that reasoning from swallowing a low target whole. A
// deployment running at a few hundred milliseconds has no cellular burst to
// absorb, and two seconds of slack there is several times the target, so drift
// would never be corrected at all.
const (
	hysteresis = 2 * time.Second
	minMargin  = 250 * time.Millisecond
)

// paceHeadroom is how much faster than the measured arrival rate the reader may
// emit. Pacing at exactly the arrival rate would turn any underestimate into a
// growing queue, and this is a smoother, not a shaper.
//
// maxPaceLag bounds the whole mechanism: no packet is ever held longer than this
// past the moment it was due, and a packet already later than this releases
// immediately with the pacer's debt cleared. One frame at 30fps, so the pacer
// can spread a frame but never becomes a second buffer, and a link recovering
// from a stall is not smoothed into staying behind.
const (
	paceHeadroom = 1.25
	maxPaceLag   = 33 * time.Millisecond
)

// resetCeiling is the depth at which a buffer stops being polite. The skip below
// is the graceful correction and it depends on the publisher answering a
// keyframe request; when that answer never comes -- a lost PLI, an encoder on a
// long GOP, a link delivering faster than its timestamps claim -- the queue goes
// on growing and every packet plays out further behind than the one before it.
// Past this the queue is dropped on the spot rather than held for a keyframe
// that may not arrive: ten seconds is already several times any delay this
// product offers, so there is nothing left in there worth playing.
const resetCeiling = 10 * time.Second

// correctionMargin is how far past its target a buffer may drift before it
// skips. It follows the target rather than being a constant, because what counts
// as drift at two seconds is the whole buffer at three hundred milliseconds.
func correctionMargin(target time.Duration) time.Duration {
	return min(hysteresis, max(target, minMargin))
}

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

	b.bytesIn += uint64(pkt.MarshalSize()) //nolint:gosec // a packet size is never negative.

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

	// Only a packet that is now due before everything else changes when the
	// reader has to wake. On a healthy link every packet lands at the back of
	// the queue, so waking on each one had the reader recompute its wait and
	// re-arm a timer several hundred times a second for no change at all.
	b.arrived++

	if b.queue.push(buffered{pkt: pkt, playAt: playAt, order: b.arrived}) {
		b.ready.Signal()
	}
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

		now := time.Now()

		wait := b.queue[0].playAt.Sub(now)
		if wait <= 0 {
			if hold := b.paceHoldLocked(now, b.queue[0].playAt); hold > 0 {
				b.waitUntilLocked(hold)

				continue
			}

			pkt := b.queue.pop().pkt
			b.chargePaceLocked(now, pkt)

			return pkt, true
		}

		b.waitUntilLocked(wait)
	}
}

// waitUntilLocked sleeps without holding the lock, waking early if a packet that
// is due sooner arrives or the buffer closes.
//
// One timer is kept and re-armed rather than a new one per wait. There is a
// single reader per buffer, so there is never more than one wait outstanding,
// and a fresh timer here was an allocation on the path every packet takes. A
// timer that fires while it is being re-armed only signals early, which the
// loop in Pop already tolerates.
func (b *Buffer) waitUntilLocked(wait time.Duration) {
	if b.timer == nil {
		b.timer = time.AfterFunc(wait, b.wake)
	} else {
		b.timer.Reset(wait)
	}

	defer b.timer.Stop()

	b.ready.Wait()
}

func (b *Buffer) wake() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.ready.Signal()
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
//
// Past resetCeiling it stops waiting for that keyframe and empties the queue,
// which is the only correction that cannot be refused by a publisher that never
// sends one. Asking again matters as much as the drop: a skip already in flight
// means the first request went unanswered, and the buffer would otherwise sit at
// ten seconds behind for as long as the encoder felt like it.
func (b *Buffer) Correct() bool {
	b.mu.Lock()

	b.measurePaceLocked(time.Now())

	depth := b.depthLocked()
	started := false

	switch {
	case depth > resetCeiling:
		b.dropAllLocked()
		b.catchUp = true
		started = true
	case b.catchUp:
		// A skip is already running, and its queue cannot grow past the ceiling:
		// inter frames are dropped while catching up, so depth reads zero here.
		// What does happen is nothing at all — the request went unanswered and the
		// picture stays frozen on the last frame that played. Asking once a second
		// costs one PLI and is the only thing that ends it.
	case depth <= b.target+correctionMargin(b.target):
		b.mu.Unlock()

		return false
	default:
		b.catchUp = true
		started = true
	}

	ask := b.keyframe
	b.mu.Unlock()

	if ask != nil {
		ask()
	}

	return started
}

// paceHoldLocked reports how long a due packet should wait so its frame leaves
// spread rather than as one burst. Zero means send it now, which is the answer
// whenever no rate has been measured, the packet is already late, or its slot
// has arrived.
func (b *Buffer) paceHoldLocked(now, playAt time.Time) time.Duration {
	if b.paceRate <= 0 {
		return 0
	}

	// Already later than a frame past its slot: the link is behind, and holding
	// anything back here would only deepen that. Clearing the debt matters as
	// much as returning zero, or the burst that follows a stall pays for a queue
	// it never built.
	if now.Sub(playAt) > maxPaceLag {
		b.nextSlot = now

		return 0
	}

	hold := b.nextSlot.Sub(now)
	if hold <= 0 {
		return 0
	}

	return min(hold, maxPaceLag)
}

// chargePaceLocked books the time this packet's own bytes occupy, which is what
// puts the next one a slot later.
func (b *Buffer) chargePaceLocked(now time.Time, pkt *rtp.Packet) {
	if b.paceRate <= 0 {
		return
	}

	if b.nextSlot.Before(now) {
		b.nextSlot = now
	}

	seconds := float64(pkt.MarshalSize()) / b.paceRate
	b.nextSlot = b.nextSlot.Add(time.Duration(seconds * float64(time.Second)))
}

// measurePaceLocked turns the bytes that arrived since the last pass into the
// rate the reader emits at. It runs on Correct's tick, so the rate follows a
// camera that changes bitrate without needing a path of its own.
func (b *Buffer) measurePaceLocked(now time.Time) {
	if b.paceAt.IsZero() {
		b.paceAt, b.bytesIn = now, 0

		return
	}

	elapsed := now.Sub(b.paceAt)
	if elapsed < correctionInterval/4 {
		return
	}

	rate := float64(b.bytesIn) / elapsed.Seconds() * paceHeadroom
	b.paceAt, b.bytesIn = now, 0

	if rate <= 0 {
		return
	}

	// Weighted toward the rate already in use: a single quiet second between
	// keyframes is not a camera that slowed down.
	if b.paceRate <= 0 {
		b.paceRate = rate
	} else {
		b.paceRate = 0.7*b.paceRate + 0.3*rate
	}
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
//
// Packets already queued move with it. They were scheduled against the old
// target, so leaving them where they are would put every packet arriving after
// a lowered delay ahead of every packet queued before it, and the relay would
// write a second of media out in reverse. The shift is the same for all of
// them, so the heap order is unchanged.
func (b *Buffer) SetTarget(target time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()

	shift := target - b.target
	b.target = target

	for idx := range b.queue {
		b.queue[idx].playAt = b.queue[idx].playAt.Add(shift)
	}

	b.ready.Signal()
}

// Close releases any blocked reader once the queue is drained.
func (b *Buffer) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.closed = true
	b.ready.Broadcast()
}

// buffered is one packet, the moment it is due, and where it sat in the arrival
// order. The order is the tie-break: every packet of a frame carries one RTP
// timestamp and therefore one playAt, so without it the heap returns them in
// whatever order its own swaps left behind. That reordering is invisible in a
// test that pushes one packet per frame and very visible on the wire, where a
// receiver handed seq 1, 10, 9 asks for retransmissions of packets that were
// never lost.
type buffered struct {
	pkt    *rtp.Packet
	playAt time.Time
	order  uint64
}

// before is the heap's ordering: due first, and among packets due together the
// one that arrived first.
func (b buffered) before(other buffered) bool {
	if b.playAt.Equal(other.playAt) {
		return b.order < other.order
	}

	return b.playAt.Before(other.playAt)
}

// packetHeap is a binary min-heap ordered by playout time, which also repairs
// reordering: a packet arriving out of order sorts back into place.
//
// container/heap would do this, but its Push and Pop take and return `any`, so
// every packet boxed a buffered value onto the heap. That was two allocations
// per packet on a path that carries several hundred a second per camera.
type packetHeap []buffered

// push adds an item and reports whether it came to rest at the root, which is
// what tells the reader its next deadline moved.
func (h *packetHeap) push(item buffered) bool {
	*h = append(*h, item)

	child := len(*h) - 1
	for child > 0 {
		parent := (child - 1) / 2
		if !(*h)[child].before((*h)[parent]) {
			break
		}

		(*h)[child], (*h)[parent] = (*h)[parent], (*h)[child]
		child = parent
	}

	return child == 0
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

		if left < size && (*h)[left].before((*h)[smallest]) {
			smallest = left
		}

		if right := left + 1; right < size && (*h)[right].before((*h)[smallest]) {
			smallest = right
		}

		if smallest == parent {
			return
		}

		(*h)[parent], (*h)[smallest] = (*h)[smallest], (*h)[parent]
		parent = smallest
	}
}
