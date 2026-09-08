// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

// Package listen runs one HTTP listener until its context ends. Three of them
// bind in this daemon, and what differs between them is an address and a word in
// the log line, not the lifecycle.
package listen

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/pion/logging"
	"golang.org/x/net/netutil"
)

// grace is how long an in-flight request has to finish once shutdown starts.
const grace = 5 * time.Second

// Serve binds addr, serves until ctx ends, then drains. The name appears in the
// log line and in any error, so a failure says which listener stopped.
func Serve(
	ctx context.Context, log logging.LeveledLogger, srv *http.Server,
	addr, name string, certificate ...string,
) error {
	srv.ReadTimeout = 10 * time.Second
	srv.WriteTimeout = 20 * time.Second
	srv.IdleTimeout = 30 * time.Second
	srv.MaxHeaderBytes = 16 << 10
	var lcfg net.ListenConfig

	listener, err := lcfg.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("%w: %s on %s: %w", ErrServe, name, addr, err)
	}

	errs := make(chan error, 1)

	listener = netutil.LimitListener(listener, 128)
	go func() {
		if len(certificate) == 2 && (certificate[0] != "" || certificate[1] != "") {
			srv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
			errs <- srv.ServeTLS(listener, certificate[0], certificate[1])
		} else {
			errs <- srv.Serve(listener)
		}
	}()
	defer func() { _ = listener.Close() }()

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

		if err := srv.Shutdown(stop); err != nil {
			_ = srv.Close()

			return err
		}

		return nil
	}
}
