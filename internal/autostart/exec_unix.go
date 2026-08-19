// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

//go:build darwin || linux

package autostart

import (
	"context"
	"fmt"
	"os/exec"
)

func run(ctx context.Context, name string, args ...string) error {
	// #nosec G204 -- name is a literal and args are paths this process built.
	if err := exec.CommandContext(ctx, name, args...).Run(); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrRegister, name, err)
	}

	return nil
}
