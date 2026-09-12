// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package relay

import (
	"math"
	"sync"
	"time"

	"github.com/Anywaystv/wagaStrim/internal/dynamicdelay"
	"github.com/pion/rtp"
)

// Buffer schedules media by RTP timestamp plus a fixed delay. Arrival-based
// scheduling would reproduce network gaps instead of absorbing late packets.
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

	// catchUp drops video until a keyframe allows the playout clock to reset.
	catchUp  bool
	keyframe func()

	// arrived counts pushes, which is what orders packets that are due together.
	arrived uint64

	// Spread each frame across its interval to avoid packet bursts. Byte-rate
	// pacing would let large keyframes delay the frames behind them.
	group    time.Time     // the playAt of the group being drained
	groupGap time.Duration // spacing between that group's packets
	frameGap time.Duration // interval between frames, learned from their playout times
	nextSlot time.Time

	late    uint64
	dropped uint64

	dynamic  *dynamicdelay.Controller
	adjust   func(time.Time)
	shift    time.Duration
	cutoff   time.Time
	peakLate time.Duration
}

// Allow recovery bursts without triggering a skip, but scale the margin down
// for low-delay deployments.
const (
	hysteresis = 2 * time.Second
	minMargin  = 250 * time.Millisecond
)

// maxPaceLag limits pacing debt to one 30fps frame; later packets bypass pacing.
const maxPaceLag = 33 * time.Millisecond

// resetCeiling drops the queue immediately rather than waiting for a keyframe.
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
		// Consume earlier jumps before anchoring a newly arrived track.
		b.dynamicOffsetLocked(time.Now())
		b.baseRTP = pkt.Timestamp
		b.baseWall = time.Now()
		b.based = true
	}

	now := time.Now()
	playAt, offset, stale := b.dynamicArrivalLocked(pkt, now)
	if stale {
		b.dropped++

		return
	}

	// Past its slot already. Queue it anyway: a late packet still beats a hole,
	// and the count is what tells the UI the target is too low for this link.
	if late := now.Sub(playAt.Add(offset)); late > 0 {
		b.late++
		b.peakLate = max(b.peakLate, late)
	}

	if b.catchUp && !isKeyframe(b.mime, pkt.Payload) {
		b.dropped++

		return
	}

	if b.catchUp {
		// Rebase the clock too, or the replacement keyframe retains the old drift.
		b.catchUp = false
		b.dropAllLocked()
		b.baseRTP = pkt.Timestamp
		b.baseWall = time.Now()
		playAt = b.playoutOf(pkt.Timestamp)
	}

	// Wake the reader only when its next deadline changes.
	b.arrived++

	if b.queue.push(buffered{pkt: pkt, playAt: playAt, order: b.arrived}) {
		b.ready.Signal()
	}
}

func (b *Buffer) dynamicArrivalLocked(pkt *rtp.Packet, now time.Time) (time.Time, time.Duration, bool) {
	offset := b.dynamicOffsetLocked(now)
	playAt := b.playoutOf(pkt.Timestamp)
	if playAt.Add(-b.target).Before(b.cutoff) {
		return playAt, offset, true
	}
	if b.dynamic == nil {
		return playAt, offset, false
	}
	state := b.dynamic.State()
	if state == nil {
		return playAt, offset, false
	}
	if state.Pending && b.keyframe != nil && isKeyframe(b.mime, pkt.Payload) {
		b.dynamic.Jump(now, playAt.Add(-b.target))
		offset = b.dynamicOffsetLocked(now)
		playAt = b.playoutOf(pkt.Timestamp)
	}

	return playAt, offset, false
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
		offset := b.dynamicOffsetLocked(now)
		if len(b.queue) == 0 {
			continue
		}

		wait := b.queue[0].playAt.Add(offset).Sub(now)
		if wait <= 0 {
			if hold := b.paceHoldLocked(now); hold > 0 {
				b.waitUntilLocked(hold)

				continue
			}

			pkt := b.queue.pop().pkt
			b.chargePaceLocked(now)

			return pkt, true
		}

		b.waitUntilLocked(wait)
	}
}

// waitUntilLocked releases the lock while waiting. A single reader reuses one
// timer to avoid per-packet allocations; Pop tolerates early wakeups.
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

func (b *Buffer) depthLocked() time.Duration {
	var newest time.Time

	for _, item := range b.queue {
		if item.playAt.After(newest) {
			newest = item.playAt
		}
	}

	return max(0, time.Until(newest))
}

// Correct requests a keyframe to recover from excess depth and reports whether
// correction started. Beyond resetCeiling it also discards queued packets.
func (b *Buffer) Correct() bool {
	if b.adjust != nil {
		b.adjust(time.Now())
	}
	b.mu.Lock()

	// Intentional dynamic buffering is not RTP clock drift. Keep the original
	// guard on the nominal queue depth so a 10-second target cannot trip it.
	b.dynamicOffsetLocked(time.Now())
	depth := b.depthLocked()
	started := false

	switch {
	case depth > resetCeiling:
		b.dropAllLocked()
		b.catchUp = true
		started = true
	case b.catchUp:
		// Retry the keyframe request: the previous PLI may have been lost.
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

// paceHoldLocked returns the pacing wait, or zero to send now. Packets sharing
// a playout time reuse the spacing calculated for the first packet.
func (b *Buffer) paceHoldLocked(now time.Time) time.Duration {
	head := b.queue[0]

	if !head.playAt.Equal(b.group) {
		b.startGroupLocked(now, head.playAt)
	}

	// Already a frame late: the link is behind and holding anything back only
	// deepens it. Clearing the slot matters as much as returning zero, or the
	// burst that follows a stall pays for a queue it never built.
	offset := time.Duration(0)
	if b.dynamic != nil {
		if state := b.dynamic.State(); state != nil {
			offset = state.Delay(now) - b.target
		}
	}
	if now.Sub(head.playAt.Add(offset)) > b.lateAllowanceLocked() {
		b.nextSlot = now

		return 0
	}

	if hold := b.nextSlot.Sub(now); hold > 0 {
		return min(hold, b.lateAllowanceLocked())
	}

	return 0
}

// startGroupLocked measures the frame now at the head: the gap to the previous
// frame is the cadence to hold, and the packets sharing this playout time are
// what has to fit inside it.
func (b *Buffer) startGroupLocked(now time.Time, playAt time.Time) {
	if !b.group.IsZero() {
		if gap := playAt.Sub(b.group); gap > 0 && gap < time.Second {
			if b.frameGap == 0 {
				b.frameGap = gap
			} else {
				b.frameGap = (3*b.frameGap + gap) / 4
			}
		}
	}

	packets := 0

	for _, item := range b.queue {
		if item.playAt.Equal(playAt) {
			packets++
		}
	}

	b.group = playAt
	b.nextSlot = now
	b.groupGap = 0

	// Four fifths of the interval, so a group always finishes before the next
	// frame is due however badly the count or the cadence is estimated. One
	// packet needs no spacing at all.
	if packets > 1 && b.frameGap > 0 {
		b.groupGap = b.frameGap * 4 / 5 / time.Duration(packets)
	}
}

// lateAllowanceLocked is how far past its playout time a packet may be held or
// arrive before pacing gives up on it. One frame, once a cadence is known.
func (b *Buffer) lateAllowanceLocked() time.Duration {
	if b.frameGap <= 0 {
		return maxPaceLag
	}

	return min(b.frameGap, maxPaceLag)
}

// chargePaceLocked books this packet's own slot, which is what puts the next
// one of the same frame a spacing later.
func (b *Buffer) chargePaceLocked(now time.Time) {
	if b.groupGap <= 0 {
		return
	}

	// Advance from the scheduled slot so timer jitter does not compound.
	// Reset only when pacing has fallen more than a frame behind.
	if b.nextSlot.Before(now.Add(-b.lateAllowanceLocked())) {
		b.nextSlot = now
	}

	b.nextSlot = b.nextSlot.Add(b.groupGap)
}

func (b *Buffer) dropAllLocked() {
	b.dropped += uint64(len(b.queue)) //nolint:gosec // a queue length is never negative.
	clear(b.queue)                    // Release packet payloads while retaining queue capacity.
	b.queue = b.queue[:0]
}

// Stats reports packets that missed their slot and packets discarded while
// catching up. A rising late count means the target is too low for this link.
func (b *Buffer) Stats() (late, dropped uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.late, b.dropped
}

// SetTarget shifts queued packets equally to preserve ordering with new arrivals.
// Callers must clamp the target before setting it.
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

// dynamicOffsetLocked applies a shared jump once, then reads the gradual curve.
func (b *Buffer) dynamicOffsetLocked(now time.Time) time.Duration {
	if b.dynamic == nil {
		return 0
	}
	state := b.dynamic.State()
	if state == nil {
		return 0
	}
	if !state.Cutoff.Equal(b.cutoff) {
		b.baseWall = b.baseWall.Add(state.Shift - b.shift)
		b.shift = state.Shift
		b.cutoff = state.Cutoff
		b.dropAllLocked()
		b.group = time.Time{}
		b.catchUp = false
		b.peakLate = 0
	}

	return state.Delay(now) - b.target
}

// Close releases any blocked reader once the queue is drained.
func (b *Buffer) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.closed = true
	b.ready.Broadcast()
}

// buffered breaks equal playout times by arrival order, preserving packet order
// within a frame and avoiding unnecessary retransmission requests.
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

// packetHeap orders packets by playout time without container/heap's per-packet
// interface boxing allocations.
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
