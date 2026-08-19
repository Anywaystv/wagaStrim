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

// skipIfRealEntry refuses to touch a LaunchAgent that belongs to the person
// running the tests.
func skipIfRealEntry(t *testing.T) string {
	t.Helper()

	path, err := plistPath()
	require.NoError(t, err)

	if _, err := os.Stat(path); err == nil {
		t.Skip("a real login entry exists on this machine; refusing to touch it")
	}

	return path
}

// The whole contract of the toggle is that unticking leaves nothing behind. An
// orphaned plist pointing at a moved binary is worse than never enabling it.
func TestDisableLeavesNothingBehind(t *testing.T) {
	path := skipIfRealEntry(t)
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

// A binary that moved leaves an entry pointing at the old path. Reporting that
// as simply on would show a tick for something that starts nothing.
func TestAMovedBinaryReadsAsStale(t *testing.T) {
	path := skipIfRealEntry(t)

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte("points at /somewhere/else"), 0o600))

	t.Cleanup(func() { _ = os.Remove(path) })

	state := Status(context.Background())
	assert.True(t, state.On)
	assert.True(t, state.Stale, "an entry naming another file must not read as healthy")
}

func TestDisableOnAMachineWithNoEntry(t *testing.T) {
	skipIfRealEntry(t)

	assert.NoError(t, Disable(context.Background()), "removing nothing is not a failure")
}
