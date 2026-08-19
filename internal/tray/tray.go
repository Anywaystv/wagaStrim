// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

//go:build !notray

// Package tray puts an icon in the system tray. It is the only package that
// links cgo, which is why it sits behind a build tag: the headless Linux build
// is a static binary and reaches the same interface through a printed URL.
package tray

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"

	"fyne.io/systray"
	"github.com/pion/logging"
)

// ErrOpenBrowser is returned when the settings page cannot be handed to the desktop.
var ErrOpenBrowser = errors.New("cannot open browser")

// Run shows the icon and blocks until the context is canceled. The caller owns
// the lifecycle; quitting from the menu cancels through stop.
func Run(ctx context.Context, uiAddr string, log logging.LeveledLogger, stop context.CancelFunc) {
	onReady := func() {
		systray.SetTitle("wagaStrim")
		systray.SetTooltip("wagaStrim — " + uiAddr)

		open := systray.AddMenuItem("Open settings", uiAddr)
		systray.AddSeparator()
		quit := systray.AddMenuItem("Quit", "Stop wagaStrim")

		go func() {
			for {
				select {
				case <-open.ClickedCh:
					if err := openBrowser(ctx, uiAddr); err != nil {
						log.Warnf("open browser: %v", err)
					}
				case <-quit.ClickedCh:
					stop()

					return
				case <-ctx.Done():
					systray.Quit()

					return
				}
			}
		}()
	}

	systray.Run(onReady, func() {})
}

// openBrowser asks the desktop to open the settings page. The URL is checked to
// be an http loopback address first, so the value reaching exec is one this
// process constructed rather than anything a caller could steer.
func openBrowser(ctx context.Context, rawURL string) error {
	// WithoutCancel so quitting wagaStrim does not close the user's browser.
	ctx = context.WithoutCancel(ctx)

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrOpenBrowser, err)
	}

	if parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" {
		return fmt.Errorf("%w: refusing to open %s", ErrOpenBrowser, parsed.Scheme)
	}

	var cmd *exec.Cmd

	// #nosec G204 -- validated above as an http loopback URL this process built.
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "open", parsed.String())
	case "windows":
		cmd = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", parsed.String())
	default:
		cmd = exec.CommandContext(ctx, "xdg-open", parsed.String())
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%w: %w", ErrOpenBrowser, err)
	}

	return nil
}
