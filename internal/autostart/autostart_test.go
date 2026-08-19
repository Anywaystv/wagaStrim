// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package autostart

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBinaryPathResolvesToARealFile(t *testing.T) {
	path, err := binaryPath()
	require.NoError(t, err)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.False(t, info.IsDir())

	assert.Equal(t, path, filepath.Clean(path), "a login entry needs a clean absolute path")
}
