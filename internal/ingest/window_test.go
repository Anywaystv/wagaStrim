// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package ingest

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The window is the skew ceiling for bonded paths, so a regression that drops it
// back to a default would silently discard the slower SIM's contribution.
func TestReplayWindowIsSizedForSkew(t *testing.T) {
	assert.Equal(t, 4096, replayWindow, "sized to tolerate several hundred ms of path skew")
	assert.Equal(t, 4096, nackHistory, "NACK history must match the buffer depth")
}
