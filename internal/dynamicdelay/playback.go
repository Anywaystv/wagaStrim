// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package dynamicdelay

// Clock maps a recent RTP timestamp to a shared audio/video clock.
type Clock struct {
	Kind        string  `json:"kind"`
	MIME        string  `json:"mime"`
	Rate        uint32  `json:"rate"`
	Timestamp   uint32  `json:"timestamp"`
	ReferenceMS float64 `json:"referenceMs"`
	Epoch       uint64  `json:"epoch"`
}

// Playback contains only the settings and clocks needed by one receiver.
type Playback struct {
	Options
	Clocks         []Clock `json:"clocks"`
	DelayMS        int     `json:"delayMs"`
	CurrentDelayMS float64 `json:"currentDelayMs"`
}
