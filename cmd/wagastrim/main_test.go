// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestListenerFailureCancelsSiblingsAndPreservesError(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	errs := make(chan error, 3)
	done := make(chan error, 1)
	go func() { done <- waitListeners(errs, 3, stop) }()
	errs <- io.ErrClosedPipe
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		require.FailNow(t, "listener failure did not cancel siblings")
	}
	errs <- nil
	errs <- io.ErrUnexpectedEOF
	select {
	case err := <-done:
		require.ErrorIs(t, err, io.ErrClosedPipe)
	case <-time.After(time.Second):
		require.FailNow(t, "listener shutdown did not complete")
	}
}

func TestListenerShutdownWithoutFailure(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	errs := make(chan error, 2)
	errs <- nil
	errs <- nil
	require.NoError(t, waitListeners(errs, 2, stop))
	require.NoError(t, ctx.Err())
}
