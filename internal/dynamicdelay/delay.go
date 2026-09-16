// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

// Package dynamicdelay adjusts a camera's shared audio/video playout delay.
package dynamicdelay

import (
	"sync"
	"sync/atomic"
	"time"
)

// MaximumMS bounds configured relay buffering, not end-to-end capture latency.
const MaximumMS = 10000

// Options are persisted with each camera. A zero value leaves adaptation off.
type Options struct {
	Enabled            bool  `json:"dynamicDelay,omitempty"`
	MaximumMS          int   `json:"maxDelayMs,omitempty"`
	Jump               bool  `json:"jumpAtMaximum"`
	CatchUpMSPerSecond int   `json:"catchUpMsPerSecond,omitempty"`
	AutoCatchUp        *bool `json:"autoCatchUp,omitempty"`
}

// Normalize supplies defaults and keeps the maximum above the normal delay.
func (o Options) Normalize(baseMS int) Options {
	if o.MaximumMS == 0 {
		o.MaximumMS = MaximumMS
		o.Jump = true
	}
	o.MaximumMS = max(1, baseMS, min(MaximumMS, o.MaximumMS))
	if o.CatchUpMSPerSecond == 0 {
		o.CatchUpMSPerSecond = 10
	}
	o.CatchUpMSPerSecond = max(1, min(200, o.CatchUpMSPerSecond))
	if o.AutoCatchUp == nil {
		automatic := true
		o.AutoCatchUp = &automatic
	}

	return o
}

// Automatic treats older camera settings as automatic until explicitly disabled.
func (o Options) Automatic() bool {
	return o.AutoCatchUp == nil || *o.AutoCatchUp
}

// CatchUp reaches 200ms/s at 90% of the space above the normal delay.
func (o Options) CatchUp(current, base time.Duration) time.Duration {
	if !o.Automatic() {
		return time.Duration(o.CatchUpMSPerSecond) * time.Millisecond
	}
	span := max(time.Millisecond, time.Duration(o.MaximumMS)*time.Millisecond-base)
	speed := min(200, 10+int(1900*max(0, current-base)/(9*span)))

	return time.Duration(speed) * time.Millisecond
}

// State is an immutable playout curve shared by both tracks.
type State struct {
	Start time.Time
	From  time.Duration
	To    time.Duration
	// Shift rebases both tracks together after a keyframe jump.
	Shift   time.Duration
	Cutoff  time.Time
	Pending bool
}

// Delay interpolates one second of gradual change without per-packet locks.
func (s *State) Delay(now time.Time) time.Duration {
	elapsed := min(time.Second, max(0, now.Sub(s.Start)))

	return s.From + time.Duration(int64(s.To-s.From)*int64(elapsed)/int64(time.Second))
}

// Controller has no timers or goroutines. The relay's existing tick updates it.
type Controller struct {
	mu         sync.Mutex
	state      atomic.Pointer[State]
	options    Options
	base       time.Duration
	goal       time.Duration
	quietUntil time.Time
	lastStep   time.Time
	shift      time.Duration
	cutoff     time.Time
}

// State returns the current curve, or nil when fixed delay is in effect.
func (c *Controller) State() *State {
	return c.state.Load()
}

// Configure changes live settings without discarding buffered media.
func (c *Controller) Configure(base time.Duration, options Options, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()

	options = options.Normalize(int(base / time.Millisecond))
	previous := c.options
	previous.CatchUpMSPerSecond = options.CatchUpMSPerSecond
	previous.AutoCatchUp = options.AutoCatchUp
	if c.base == base && previous == options {
		// Changing speed must preserve growth, quiet time and a pending jump.
		c.options = options

		return
	}
	c.base, c.options = base, options
	c.goal = base
	state := c.state.Load()
	if state == nil && !options.Enabled {
		return
	}
	current := base
	if state != nil && options.Enabled {
		current = state.Delay(now)
	}
	c.state.Store(&State{Start: now, From: current, To: current, Shift: c.shift, Cutoff: c.cutoff})
}

// Step reacts to the worst late arrival across both tracks. Growth is limited
// to 50ms/s; catch-up uses the camera's configured rate.
func (c *Controller) Step(now time.Time, late time.Duration) *State {
	c.mu.Lock()
	defer c.mu.Unlock()

	state := c.state.Load()
	// A keyframe can jump while the relay is gathering a lateness sample.
	if state == nil || now.Before(state.Cutoff) || now.Sub(c.lastStep) < time.Second {
		return state
	}
	c.lastStep = now
	if !c.options.Enabled {
		c.state.Store(nil)

		return nil
	}
	current := state.Delay(now)
	next := *state
	next.Start, next.From = now, current
	exceeded := c.updateGoal(now, current, late)
	next.Pending = state.Pending || (c.options.Jump && exceeded)
	catchUp := c.options.CatchUp(current, c.base)
	next.To = max(current-catchUp, min(current+50*time.Millisecond, c.goal))
	c.state.Store(&next)

	return &next
}

func (c *Controller) updateGoal(now time.Time, current, late time.Duration) bool {
	maximum := time.Duration(c.options.MaximumMS) * time.Millisecond
	wanted := current
	if late > 0 {
		wanted += late + 100*time.Millisecond
		c.goal = max(c.goal, min(maximum, wanted))
		c.quietUntil = now.Add(5 * time.Second)
	} else if now.After(c.quietUntil) {
		c.goal = c.base
	}

	return wanted > maximum
}

// Jump rebases at a received keyframe. Both tracks apply the same clock shift.
func (c *Controller) Jump(now, mediaTime time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()

	state := c.state.Load()
	if state == nil || !state.Pending {
		return
	}
	next := State{Start: now, From: c.base, To: c.base, Shift: state.Shift + now.Sub(mediaTime), Cutoff: now}
	c.shift, c.cutoff = next.Shift, next.Cutoff
	c.goal = c.base
	c.quietUntil = now.Add(5 * time.Second)
	c.state.Store(&next)
}
