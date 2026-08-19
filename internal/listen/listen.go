// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

// Package listen runs one HTTP listener until its context ends. Three of them
// bind in this daemon, and what differs between them is an address and a word in
// the log line, not the lifecycle.
package listen

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/pion/logging"
)

// grace is how long an in-flight request has to finish once shutdown starts.
const grace = 5 * time.Second

// Serve binds addr, serves until ctx ends, then drains. The name appears in the
// log line and in any error, so a failure says which listener stopped.
func Serve(ctx context.Context, log logging.LeveledLogger, srv *http.Server, addr, name string) error {
	var lcfg net.ListenConfig

	listener, err := lcfg.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("%w: %s on %s: %w", ErrServe, name, addr, err)
	}

	errs := make(chan error, 1)

	go func() { errs <- srv.Serve(listener) }()

	log.Infof("%s on %s", name, listener.Addr())

	select {
	case err := <-errs:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}

		return fmt.Errorf("%w: %s: %w", ErrServe, name, err)
	case <-ctx.Done():
		stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), grace)
		defer cancel()

		return srv.Shutdown(stop)
	}
}
