// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

// Command wagastrim runs the ingest daemon: a settings UI on loopback and, from
// phase 2, a WebRTC signaling listener that is reachable from the internet.
package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/MarcFryd/wagaStrim/internal/config"
	"github.com/MarcFryd/wagaStrim/internal/tray"
	"github.com/MarcFryd/wagaStrim/internal/ui"
	"github.com/pion/logging"
)

func main() {
	headless := flag.Bool("headless", runtime.GOOS == "linux",
		"run without a tray icon; the settings page is still served on loopback")
	flag.Parse()

	// pion's factory defaults to silence unless PION_LOG_* is set. In headless
	// mode the startup line carries the settings URL, so it has to be visible.
	factory := logging.NewDefaultLoggerFactory()
	factory.DefaultLogLevel = logging.LogLevelInfo
	log := factory.NewLogger("wagastrim")

	if err := run(*headless, log); err != nil {
		log.Errorf("%v", err)
		os.Exit(1)
	}
}

func run(headless bool, log logging.LeveledLogger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log.Infof("config %s", cfg.Path())

	srv, err := ui.New(cfg, log)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errs := make(chan error, 1)

	go func() { errs <- srv.Serve(ctx) }()

	if headless {
		log.Infof("headless; open %s", srv.Addr())
		<-ctx.Done()

		return <-errs
	}

	// systray must own the main thread on macOS, so the UI runs in the goroutine
	// above and this call blocks here until the icon is dismissed.
	tray.Run(ctx, srv.Addr(), log, stop)

	return <-errs
}
