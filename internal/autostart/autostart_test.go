// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package autostart

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// entryPath is where this platform keeps its login entry, or empty when the
// entry is not a file at all.
func entryPath(t *testing.T) string {
	t.Helper()

	switch {
	case hasPlist():
		path, err := plistPath()
		require.NoError(t, err)

		return path
	default:
		return ""
	}
}

func TestBinaryPathResolvesToARealFile(t *testing.T) {
	path, err := binaryPath()
	require.NoError(t, err)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.False(t, info.IsDir())

	assert.Equal(t, path, filepath.Clean(path), "a login entry needs a clean absolute path")
}

// The whole contract of the toggle is that unticking leaves nothing behind. An
// orphaned unit or plist pointing at a moved binary is worse than never having
// enabled it.
func TestDisableLeavesNothingBehind(t *testing.T) {
	path := entryPath(t)
	if path == "" {
		t.Skip("this platform does not keep its login entry in a file")
	}

	if _, err := os.Stat(path); err == nil {
		t.Skip("a real login entry exists on this machine; refusing to touch it")
	}

	ctx := context.Background()

	require.NoError(t, Enable(ctx))

	body, err := os.ReadFile(path) //nolint:gosec // path came from the package itself.
	require.NoError(t, err)

	binary, err := binaryPath()
	require.NoError(t, err)
	assert.Contains(t, string(body), binary, "the entry must name this binary")

	state := Status(ctx)
	assert.True(t, state.On)
	assert.False(t, state.Stale, "a freshly written entry is not stale")

	require.NoError(t, Disable(ctx))

	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err), "unticking must remove the file, not just unload it")
	assert.False(t, Status(ctx).On)
}

// A binary that moved leaves an entry pointing at the old path. Reporting it as
// simply "on" would show a tick for something that starts nothing.
func TestAMovedBinaryReadsAsStale(t *testing.T) {
	path := entryPath(t)
	if path == "" {
		t.Skip("this platform does not keep its login entry in a file")
	}

	if _, err := os.Stat(path); err == nil {
		t.Skip("a real login entry exists on this machine; refusing to touch it")
	}

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte("points at /somewhere/else/wagastrim"), 0o600))

	t.Cleanup(func() { _ = os.Remove(path) })

	state := Status(context.Background())
	assert.True(t, state.On)
	assert.True(t, state.Stale, "an entry naming another file must not read as healthy")
}

func TestDisableOnAMachineWithNoEntry(t *testing.T) {
	path := entryPath(t)
	if path == "" {
		t.Skip("this platform does not keep its login entry in a file")
	}

	if _, err := os.Stat(path); err == nil {
		t.Skip("a real login entry exists on this machine; refusing to touch it")
	}

	assert.NoError(t, Disable(context.Background()), "removing nothing is not a failure")
}
