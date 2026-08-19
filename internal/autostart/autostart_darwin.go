// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package autostart

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const agentLabel = "tv.anyways.wagastrim"

func plistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrRegister, err)
	}

	return filepath.Join(home, "Library", "LaunchAgents", agentLabel+".plist"), nil
}

// Status reports whether a LaunchAgent exists and still points at this binary.
func Status(_ context.Context) State {
	path, err := plistPath()
	if err != nil {
		return State{}
	}

	body, err := os.ReadFile(path) //nolint:gosec // path is built from UserHomeDir.
	if err != nil {
		return State{}
	}

	want, err := binaryPath()
	if err != nil {
		return State{On: true, Stale: true}
	}

	return State{On: true, Stale: !strings.Contains(string(body), want)}
}

// Enable writes a LaunchAgent and loads it. Rewriting an existing one is how a
// stale entry is repaired after the binary moves.
func Enable(ctx context.Context) error {
	path, err := plistPath()
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

	if err := os.WriteFile(path, []byte(plist(binary)), 0o600); err != nil {
		return fmt.Errorf("%w: %w", ErrRegister, err)
	}

	// Unload first so repairing a stale entry replaces the running definition
	// rather than leaving the old one loaded.
	_ = run(ctx, "launchctl", "bootout", domain()+"/"+agentLabel)

	if err := run(ctx, "launchctl", "bootstrap", domain(), path); err != nil {
		return fmt.Errorf("%w: %w", ErrRegister, err)
	}

	return nil
}

// Disable unloads the agent and deletes the file, leaving nothing behind.
func Disable(ctx context.Context) error {
	path, err := plistPath()
	if err != nil {
		return err
	}

	_ = run(ctx, "launchctl", "bootout", domain()+"/"+agentLabel)

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("%w: %w", ErrUnregister, err)
	}

	return nil
}

func domain() string {
	return "gui/" + strconv.Itoa(os.Getuid())
}

func plist(binary string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>` + agentLabel + `</string>
  <key>ProgramArguments</key>
  <array><string>` + binary + `</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
</dict>
</plist>
`
}
