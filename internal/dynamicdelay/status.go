// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package dynamicdelay

import "time"

// Status describes relay scheduling, not an individual viewer's playback speed.
type Status struct {
	Enabled            bool    `json:"dynamicDelay"`
	DelayMS            int     `json:"delayMs"`
	CurrentDelayMS     float64 `json:"currentDelayMs"`
	CatchUpMSPerSecond float64 `json:"catchUpMsPerSecond"`
	JumpPending        bool    `json:"jumpPending"`
}

// Status reads the active curve so a saved speed limit is never reported as activity.
func (c *Controller) Status(now time.Time) Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	status := Status{
		Enabled: c.options.Enabled, DelayMS: int(c.base / time.Millisecond),
		CurrentDelayMS: float64(c.base) / float64(time.Millisecond),
	}
	if state := c.state.Load(); state != nil && status.Enabled {
		status.CurrentDelayMS = float64(state.Delay(now)) / float64(time.Millisecond)
		status.JumpPending = state.Pending
		if !now.Before(state.Start) && now.Sub(state.Start) < time.Second {
			status.CatchUpMSPerSecond = float64(max(0, state.From-state.To)) / float64(time.Millisecond)
		}
	}

	return status
}
