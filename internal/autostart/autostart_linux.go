// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package autostart

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const unitName = "wagastrim.service"

func unitPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrRegister, err)
	}

	return filepath.Join(dir, "systemd", "user", unitName), nil
}

// lingerNote is the part people get wrong. A user service starts when the user
// logs in, and a headless box next to the router never does, so the unit sits
// there doing nothing and looks broken.
const lingerNote = "On a headless machine also run: loginctl enable-linger $USER. " +
	"Without it a user service only starts when someone logs in."

// Status reports whether the unit exists and still points at this binary.
func Status(_ context.Context) State {
	path, err := unitPath()
	if err != nil {
		return State{}
	}

	body, err := os.ReadFile(path) //nolint:gosec // path is built from UserConfigDir.
	if err != nil {
		return State{}
	}

	want, err := binaryPath()
	if err != nil {
		return State{On: true, Stale: true, Note: lingerNote}
	}

	return State{On: true, Stale: !strings.Contains(string(body), want), Note: lingerNote}
}

// Enable writes a user unit and starts it.
func Enable(ctx context.Context) error {
	path, err := unitPath()
	if err != nil {
		return err
	}

	binary, err := binaryPath()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("%w: %w", ErrRegister, err)
	}

	if err := os.WriteFile(path, []byte(unit(binary)), 0o600); err != nil {
		return fmt.Errorf("%w: %w", ErrRegister, err)
	}

	// Reload so a rewritten unit replaces the old definition rather than
	// leaving systemd running the previous binary path.
	_ = run(ctx, "systemctl", "--user", "daemon-reload")

	if err := run(ctx, "systemctl", "--user", "enable", "--now", unitName); err != nil {
		return fmt.Errorf("%w: %w", ErrRegister, err)
	}

	return nil
}

// Disable stops the unit and deletes it, leaving no orphaned file behind.
func Disable(ctx context.Context) error {
	path, err := unitPath()
	if err != nil {
		return err
	}

	_ = run(ctx, "systemctl", "--user", "disable", "--now", unitName)

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("%w: %w", ErrUnregister, err)
	}

	_ = run(ctx, "systemctl", "--user", "daemon-reload")

	return nil
}

// unit runs headless: the tray needs a desktop session, and the machine this
// deployment suits best does not have one.
func unit(binary string) string {
	return `[Unit]
Description=wagaStrim ingest
After=network-online.target

[Service]
ExecStart=` + binary + ` -headless
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`
}
