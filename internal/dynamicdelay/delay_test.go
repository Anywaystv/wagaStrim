// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package dynamicdelay

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultsAndBounds(t *testing.T) {
	defaults := (Options{}).Normalize(2000)
	assert.False(t, defaults.Enabled)
	assert.Equal(t, MaximumMS, defaults.MaximumMS)
	assert.True(t, defaults.Jump)
	assert.Equal(t, 10, defaults.CatchUpMSPerSecond)
	assert.Equal(t, 1, (Options{CatchUpMSPerSecond: -1}).Normalize(2000).CatchUpMSPerSecond)
	assert.Equal(t, 100, (Options{CatchUpMSPerSecond: 999}).Normalize(2000).CatchUpMSPerSecond)
	assert.Equal(t, 2000, (Options{MaximumMS: 100}).Normalize(2000).MaximumMS)
	assert.Equal(t, MaximumMS, (Options{MaximumMS: 20000}).Normalize(2000).MaximumMS)
	assert.False(t, (Options{MaximumMS: 10000, Jump: false}).Normalize(2000).Jump)
	assert.Equal(t, 1, (Options{MaximumMS: -1}).Normalize(0).MaximumMS)
}

func TestFixedDelayDoesNotCreateACurve(t *testing.T) {
	var control Controller
	now := time.Unix(100, 0)
	control.Configure(2*time.Second, Options{}, now)
	assert.Nil(t, control.State())
	assert.Nil(t, control.Step(now, 5*time.Second))
}

func TestLateArrivalGrowsThenCatchesUpWithoutJump(t *testing.T) {
	var control Controller
	now := time.Unix(100, 0)
	control.Configure(2*time.Second, Options{Enabled: true, MaximumMS: 10000, Jump: true}, now)
	initial := control.Step(now, 300*time.Millisecond)
	require.NotNil(t, initial)
	assert.Equal(t, 2025*time.Millisecond, initial.Delay(now.Add(500*time.Millisecond)))
	previous := initial.Delay(now.Add(time.Second))
	for second := 1; second <= 40; second++ {
		at := now.Add(time.Duration(second) * time.Second)
		state := control.Step(at, 0)
		delay := state.Delay(at.Add(time.Second))
		assert.GreaterOrEqual(t, delay, 2*time.Second)
		assert.LessOrEqual(t, delay-previous, 50*time.Millisecond)
		assert.GreaterOrEqual(t, delay-previous, -10*time.Millisecond)
		assert.False(t, state.Pending)
		previous = delay
	}
	assert.Equal(t, 2*time.Second, previous)
}

func TestCeilingJumpCanBeDisabledAndPreservesClockAcrossToggle(t *testing.T) {
	var control Controller
	now := time.Unix(100, 0)
	options := Options{Enabled: true, MaximumMS: 3000, Jump: true}
	control.Configure(2*time.Second, options, now)
	require.True(t, control.Step(now, 2*time.Second).Pending)
	options.Jump = false
	control.Configure(2*time.Second, options, now)
	assert.False(t, control.State().Pending)
	assert.False(t, control.Step(now.Add(time.Second), 2*time.Second).Pending)
	options.Jump = true
	control.Configure(2*time.Second, options, now.Add(2*time.Second))
	require.True(t, control.Step(now.Add(2*time.Second), 2*time.Second).Pending)
	control.Jump(now.Add(2*time.Second), now)
	assert.Equal(t, 2*time.Second, control.State().Shift)
	assert.Equal(t, 2*time.Second, control.State().Delay(now.Add(2*time.Second)))
	assert.False(t, control.State().Pending)
	options.Enabled = false
	control.Configure(2*time.Second, options, now.Add(3*time.Second))
	control.Step(now.Add(4*time.Second), 0)
	assert.Nil(t, control.State())
	options.Enabled = true
	control.Configure(2*time.Second, options, now.Add(5*time.Second))
	assert.Equal(t, 2*time.Second, control.State().Shift)
}

func TestUnchangedConfigurationDoesNotInterruptGrowth(t *testing.T) {
	var control Controller
	now := time.Unix(100, 0)
	options := Options{Enabled: true, MaximumMS: 10000, Jump: true}
	control.Configure(time.Second, options, now)
	first := control.Step(now, 300*time.Millisecond)
	control.Configure(time.Second, options, now.Add(500*time.Millisecond))
	assert.Same(t, first, control.State())
}

func TestPersistentLatenessStopsAtTheMaximumWithoutJumping(t *testing.T) {
	var control Controller
	now := time.Unix(100, 0)
	control.Configure(2*time.Second, Options{Enabled: true, MaximumMS: 3000}, now)
	for second := range 100 {
		at := now.Add(time.Duration(second) * time.Second)
		state := control.Step(at, 2*time.Second)
		assert.LessOrEqual(t, state.Delay(at.Add(time.Second)), 3*time.Second)
		assert.False(t, state.Pending)
	}
	assert.Equal(t, 3*time.Second, control.State().To)
}

func BenchmarkReadCurve(b *testing.B) {
	var control Controller
	now := time.Now()
	control.Configure(time.Second, Options{Enabled: true, MaximumMS: 10000}, now)
	control.Step(now, 100*time.Millisecond)
	b.ReportAllocs()
	for range b.N {
		control.State().Delay(now)
	}
}

func TestSampleStartedBeforeJumpCannotScheduleAnotherJump(t *testing.T) {
	var control Controller
	now := time.Unix(100, 0)
	control.Configure(time.Second, Options{Enabled: true, MaximumMS: 2000, Jump: true}, now)
	require.True(t, control.Step(now, 3*time.Second).Pending)
	control.Jump(now.Add(3*time.Second), now)
	jump := control.State()
	assert.Same(t, jump, control.Step(now.Add(2*time.Second), 3*time.Second))
	assert.False(t, control.Step(now.Add(4*time.Second), 0).Pending)
}

func TestCatchUpRateAndLiveChangesPreserveTheTarget(t *testing.T) {
	for _, rate := range []int{1, 10, 100} {
		var control Controller
		now := time.Unix(100, 0)
		options := Options{Enabled: true, MaximumMS: 10000, CatchUpMSPerSecond: rate}
		control.Configure(3*time.Second, options, now)
		control.Configure(2*time.Second, options, now)
		state := control.Step(now, 0)
		assert.Equal(t, 3*time.Second-time.Duration(rate)*time.Millisecond, state.Delay(now.Add(time.Second)))
		at := now.Add(500 * time.Millisecond)
		before := state.Delay(at)
		options.CatchUpMSPerSecond = 100
		control.Configure(2*time.Second, options, at)
		assert.Equal(t, before, control.State().Delay(at), "changing speed must not jump the current delay")
		for second := 1; second <= 12; second++ {
			state = control.Step(now.Add(time.Duration(second)*time.Second), 0)
			assert.GreaterOrEqual(t, state.To, 2*time.Second)
			assert.LessOrEqual(t, state.From-state.To, 100*time.Millisecond)
			assert.False(t, state.Pending)
		}
		assert.Equal(t, 2*time.Second, state.To)
	}
}

func TestChangingSpeedPreservesRecoveryInProgress(t *testing.T) {
	for _, late := range []time.Duration{300 * time.Millisecond, 2 * time.Second} {
		var control Controller
		now := time.Unix(100, 0)
		options := Options{Enabled: true, MaximumMS: 3000, Jump: true}
		control.Configure(2*time.Second, options, now)
		before := control.Step(now, late)
		options.CatchUpMSPerSecond = 100
		control.Configure(2*time.Second, options, now.Add(500*time.Millisecond))
		assert.Equal(t, before.Pending, control.State().Pending, "speed changes must not cancel a requested jump")
		after := control.Step(now.Add(time.Second), 0)
		assert.Greater(t, after.To, after.From, "the existing late-arrival target must survive a speed change")
	}
}
