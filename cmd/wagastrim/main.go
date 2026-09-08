// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

// Command wagastrim runs the ingest daemon: a settings UI on loopback and, from
// phase 2, a WebRTC signaling listener that is reachable from the internet.
package main

import (
	"context"
	"flag"
	"os"
	signalpkg "os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/MarcFryd/wagaStrim/internal/config"
	"github.com/MarcFryd/wagaStrim/internal/control"
	"github.com/MarcFryd/wagaStrim/internal/egress"
	"github.com/MarcFryd/wagaStrim/internal/ingest"
	"github.com/MarcFryd/wagaStrim/internal/relay"
	"github.com/MarcFryd/wagaStrim/internal/signal"
	"github.com/MarcFryd/wagaStrim/internal/stats"
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

	counters := stats.New()
	hub := relay.New()

	// Wired after the servers exist; the UI only calls these from handlers.
	var (
		whip *ingest.Server
		whep *egress.Server
	)

	revoke := func(id string) {
		whip.CloseIngest(id)
		whep.CloseIngest(id)
	}
	retarget := func(id string, delayMS int) {
		hub.Retarget(id, time.Duration(delayMS)*time.Millisecond)
	}

	srv, err := ui.New(cfg, log, counters, revoke, retarget)
	if err != nil {
		return err
	}

	engine, mux, err := ingest.NewSettingEngine(cfg.MediaPort, cfg.ICEPublicIPs...)
	if err != nil {
		return err
	}

	defer func() {
		if closeErr := mux.Close(); closeErr != nil {
			log.Warnf("close media mux: %v", closeErr)
		}
	}()

	whip, err = ingest.NewServer(cfg, log, engine, hub, counters,
		func(id string) { whep.CloseIngest(id) })
	if err != nil {
		return err
	}

	defer whip.Close()

	whep = egress.NewServer(cfg, log, whip.API(), hub)
	defer whep.Close()

	public := signal.New(cfg, log, whip, whep)

	ctx, stop := signalpkg.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errs := make(chan error, 3)
	listeners := 2

	go func() { errs <- srv.Serve(ctx) }()
	go func() { errs <- public.Serve(ctx) }()

	// Only where a deployment configured a token. A desktop install has no
	// second machine that owns its cameras, so it never opens this port.
	if cfg.ControlToken != "" {
		listeners++
		control := control.New(cfg, log, counters, revoke)

		go func() { errs <- control.Serve(ctx) }()
	}

	finished := make(chan error, 1)
	go func() { finished <- waitListeners(errs, listeners, stop) }()

	log.Infof("media on udp/%d, forward it along with tcp/%d", cfg.MediaPort, cfg.SignalPort)

	if headless {
		log.Infof("headless; open %s", srv.Addr())
		<-ctx.Done()

		return <-finished
	}

	// systray must own the main thread on macOS, so the UI runs in the goroutine
	// above and this call blocks here until the icon is dismissed.
	tray.Run(ctx, srv.Addr(), log, stop)

	return <-finished
}

// Cancel sibling listeners on failure and drain all results before closing media.
func waitListeners(errs <-chan error, count int, stop context.CancelFunc) error {
	var first error
	for range count {
		if err := <-errs; err != nil {
			if first == nil {
				first = err
			}
			stop()
		}
	}

	return first
}
