// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

// Package autostart registers the binary to run at login. Each platform has one
// file implementing the same functions, using the mechanism that platform
// already has. Nothing here needs elevated privileges, installs anything system
// wide, or survives unticking the box.
package autostart

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var (
	// ErrRegister is returned when the login entry cannot be written.
	ErrRegister = errors.New("cannot register at login")
	// ErrUnregister is returned when the login entry cannot be removed.
	ErrUnregister = errors.New("cannot remove login entry")
	// ErrLocateBinary is returned when the running executable cannot be found.
	ErrLocateBinary = errors.New("cannot locate this executable")
)

// State is what the settings page needs to render the toggle.
type State struct {
	On bool `json:"on"`
	// Stale means an entry exists but points somewhere else, which happens when
	// the binary is moved or replaced by a new build. The entry would start the
	// wrong file or nothing at all, so the UI has to offer to repair it rather
	// than show a tick that lies.
	Stale bool   `json:"stale"`
	Note  string `json:"note,omitempty"`
}

// binaryPath is the file a login entry should point at. Symlinks are resolved,
// because an entry pointing at a symlink breaks when the link is replaced,
// which is what an upgrade does.
func binaryPath() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrLocateBinary, err)
	}

	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path, nil //nolint:nilerr // an unresolvable path is still usable.
	}

	return resolved, nil
}
