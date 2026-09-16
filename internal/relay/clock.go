// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package relay

import "time"

// Keep a correction through an outage and the slower delivery that follows it.
const clockRecoveryWindow = 30 * time.Second

// correctClocks preserves the track offset when excess depth requires a reset.
// Rebasing each track on its next arrival would make network jitter permanent.
func (s *Stream) correctClocks() bool {
	s.mu.Lock()
	now := time.Now()
	for _, buf := range s.buffers {
		buf.mu.Lock()
		buf.dynamicOffsetLocked(now)
	}

	senderClocks := s.alignSenderClocksLocked()
	shift, waiting := s.clockCorrectionLocked(now, senderClocks)
	if senderClocks {
		s.senderBaseWall = s.senderBaseWall.Add(-shift)
	}
	for _, buf := range s.buffers {
		// Sender reports define one timeline, so recovery must move both tracks.
		// Without them, leave a healthy track alone when only its sibling jumps.
		if shift > 0 && (senderClocks ||
			(!buf.recoveryAt.Equal(s.recoveryAt) && buf.depthLocked()-buf.target > minMargin)) {
			buf.moveClockLocked(-shift, true)
			buf.recoveryAt = s.recoveryAt
		}
		buf.mu.Unlock()
	}
	s.mu.Unlock()
	if shift > 0 || waiting {
		s.askKeyframe()
	}

	return shift > 0
}

// Align scheduling with the same sender clock used by the player. Correcting
// only the player leaves one track arriving after its presentation deadline.
func (s *Stream) alignSenderClocksLocked() bool {
	if len(s.buffers) < 2 {
		return false
	}
	for _, buf := range s.buffers {
		if !buf.based || buf.senderNTP == 0 {
			return false
		}
	}
	established := !s.senderBaseWall.IsZero()
	if !established {
		anchor := s.buffers[0]
		s.senderBaseMS = anchor.senderTimeMS(anchor.baseRTP)
		s.senderBaseWall = anchor.baseWall.Add(-anchor.shift)
	}
	for _, buf := range s.buffers {
		delta := time.Duration((buf.senderTimeMS(buf.baseRTP) - s.senderBaseMS) * float64(time.Millisecond))
		buf.alignSenderClockLocked(s.senderBaseWall.Add(delta+buf.shift), established)
	}

	return true
}

func (b *Buffer) alignSenderClockLocked(base time.Time, established bool) {
	shift := base.Sub(b.baseWall)
	if shift.Abs() <= 50*time.Millisecond && !b.clockReset {
		return
	}
	// A timestamp reset invalidates queued media from the old mapping.
	b.moveClockLocked(shift, b.clockReset || (established && shift.Abs() > correctionMargin(b.target)))
	if b.clockReset {
		// Publish the new epoch only after old queued timestamps are gone.
		b.clockEpoch++
		b.clockReset = false
		b.resetPending = true
	}
}

func (b *Buffer) staleResetPacketLocked(sequence uint16, playAt, now time.Time) bool {
	if b.clockEpoch == 0 {
		return false
	}
	if b.resetPending || sequence-b.resetSequence >= 0x8000 {
		// Old timestamps can look hours ahead under the new sender report.
		// Keep reordered packets, including keyframe parameters, when their
		// timestamps still fit the new clock.
		return playAt.Sub(now) > b.target+correctionMargin(b.target)
	}
	b.resetSequence = sequence

	return false
}

func (b *Buffer) moveClockLocked(shift time.Duration, reset bool) {
	b.baseWall = b.baseWall.Add(shift)
	if reset {
		b.dropAllLocked()
		b.catchUp = true
	} else {
		for idx := range b.queue {
			b.queue[idx].playAt = b.queue[idx].playAt.Add(shift)
		}
	}
	b.group = time.Time{}
	b.peakLate = 0
	b.ready.Signal()
}

func (s *Stream) clockCorrectionLocked(now time.Time, senderClocks bool) (time.Duration, bool) {
	shift := time.Duration(0)
	waiting := false
	fresh := senderClocks || now.Sub(s.recoveryAt) >= clockRecoveryWindow
	for _, buf := range s.buffers {
		excess := buf.depthLocked() - buf.target
		if excess > correctionMargin(buf.target) {
			shift = max(shift, excess)
			fresh = fresh || buf.recoveryAt.Equal(s.recoveryAt)
		}
		waiting = waiting || buf.catchUp
	}
	if shift == 0 {
		return 0, waiting
	}
	// A sibling may reach the same timestamp jump only after its link recovers.
	// Reuse the recent correction instead of measuring that delayed arrival.
	if fresh {
		s.recoveryAt, s.recoveryShift = now, shift
	}

	return s.recoveryShift, waiting
}
