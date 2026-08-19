// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

//go:build notray

// Package tray is a no-op in headless builds. Linux ships this variant by
// default: tray icons there need StatusNotifierItem, a GNOME shell extension,
// and cgo, and a printed URL is a complete interface for a web UI.
package tray

import (
	"context"

	"github.com/pion/logging"
)

// Run blocks until the context is canceled and shows nothing.
func Run(ctx context.Context, uiAddr string, log logging.LeveledLogger, _ context.CancelFunc) {
	log.Infof("headless build, no tray; open %s", uiAddr)
	<-ctx.Done()
}
