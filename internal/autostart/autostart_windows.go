// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package autostart

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// runKey is the per-user Run key. It needs no administrator rights, which the
// machine-wide equivalent under HKLM would.
const (
	runKey    = `Software\Microsoft\Windows\CurrentVersion\Run`
	valueName = "wagaStrim"
)

// Windows keeps the tray: unlike the headless Linux unit, a Windows machine
// running this is the streaming PC and has a desktop to put an icon on.

// Status reports whether the Run value exists and still points at this binary.
func Status(_ context.Context) State {

	key, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return State{}
	}

	defer func() { _ = key.Close() }()

	existing, _, err := key.GetStringValue(valueName)
	if err != nil {
		return State{}
	}

	want, err := binaryPath()
	if err != nil {
		return State{On: true, Stale: true}
	}

	return State{On: true, Stale: !strings.Contains(existing, want)}
}

// Enable writes the Run value, quoting the path so a space in it does not split
// the command.
func Enable(_ context.Context) error {

	binary, err := binaryPath()
	if err != nil {
		return err
	}

	key, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrRegister, err)
	}

	defer func() { _ = key.Close() }()

	if err := key.SetStringValue(valueName, `"`+binary+`"`); err != nil {
		return fmt.Errorf("%w: %w", ErrRegister, err)
	}

	return nil
}

// Disable removes the value. A missing value is already the wanted state.
func Disable(_ context.Context) error {

	key, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return nil //nolint:nilerr // no key means nothing to remove.
	}

	defer func() { _ = key.Close() }()

	if err := key.DeleteValue(valueName); err != nil && !strings.Contains(err.Error(), "cannot find") {
		return fmt.Errorf("%w: %w", ErrUnregister, err)
	}

	return nil
}
