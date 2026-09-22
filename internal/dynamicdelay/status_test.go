// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package dynamicdelay

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestStatusReportsActiveCatchUpRatherThanTheConfiguredLimit(t *testing.T) {
	now := time.Unix(100, 0)
	var control Controller
	options := Options{Enabled: true, MaximumMS: 10000, CatchUpMSPerSecond: 200, AutoCatchUp: new(false)}
	control.Configure(2010*time.Millisecond, options, now)
	assert.Zero(t, control.Status(now).CatchUpMSPerSecond, "enabling catch-up does not mean it is running")
	control.Configure(2*time.Second, options, now)
	control.Step(now, 0)
	status := control.Status(now.Add(500 * time.Millisecond))
	assert.True(t, status.Enabled)
	assert.Equal(t, 2000, status.DelayMS)
	assert.Equal(t, float64(2005), status.CurrentDelayMS)
	assert.Equal(t, float64(10), status.CatchUpMSPerSecond, "only the remaining 10 ms can be removed")
	assert.Zero(t, control.Status(now.Add(time.Second)).CatchUpMSPerSecond, "an expired curve is no longer catching up")
	options.Enabled = false
	control.Configure(2*time.Second, options, now)
	assert.Equal(t, Status{DelayMS: 2000, CurrentDelayMS: 2000}, control.Status(now))
}

func TestStatusFollowsGrowthAutomaticRecoveryAndPendingJumps(t *testing.T) {
	now := time.Unix(100, 0)
	var control Controller
	options := Options{Enabled: true, MaximumMS: 3000, Jump: true}
	control.Configure(2*time.Second, options, now)
	control.Step(now, 300*time.Millisecond)
	growing := control.Status(now.Add(500 * time.Millisecond))
	assert.Equal(t, float64(2025), growing.CurrentDelayMS)
	assert.Zero(t, growing.CatchUpMSPerSecond)
	assert.False(t, growing.JumpPending)
	control.Step(now.Add(6*time.Second), 0)
	recovering := control.Status(now.Add(6500 * time.Millisecond))
	assert.Greater(t, recovering.CatchUpMSPerSecond, float64(0))
	assert.Less(t, recovering.CurrentDelayMS, float64(2050))
	control.Step(now.Add(7*time.Second), 2*time.Second)
	assert.True(t, control.Status(now.Add(7*time.Second)).JumpPending)
	control.Jump(now.Add(8*time.Second), now)
	jumped := control.Status(now.Add(8 * time.Second))
	assert.False(t, jumped.JumpPending)
	assert.Equal(t, float64(2000), jumped.CurrentDelayMS)
	assert.Zero(t, jumped.CatchUpMSPerSecond)
}
