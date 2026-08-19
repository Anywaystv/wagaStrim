// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package ingest

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The point of the probe is to report what the kernel granted rather than what
// was asked for, because SetReadBuffer succeeds either way and a clamped buffer
// drops packets with nothing logging it.
func TestProbeReportsWhatTheKernelGranted(t *testing.T) {
	granted, err := ProbeSocketBuffer(socketBuffer)
	require.NoError(t, err)

	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		assert.Zero(t, granted, "zero means not measured on this platform")

		return
	}

	assert.Positive(t, granted, "a real socket always has some receive buffer")
}

// A request the kernel cannot possibly honor must come back smaller, not as a
// success. If this ever returns the requested size, the readback is not working
// and the whole check is decorative.
func TestAnAbsurdRequestIsClamped(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("readback is only implemented on darwin and linux")
	}

	const absurd = 1 << 30

	granted, err := ProbeSocketBuffer(absurd)
	require.NoError(t, err)

	assert.Less(t, granted, absurd, "the kernel caps this and the probe must show it")
}
