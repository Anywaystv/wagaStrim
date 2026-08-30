// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

//go:build darwin || linux

package autostart

import (
	"context"
	"fmt"
	"os/exec"
)

// run invokes the platform's service manager. Which one that is is fixed per
// platform rather than passed in: every caller on linux said "systemctl" and
// every caller on darwin said "launchctl", so the parameter carried no
// information and unparam said so.
func run(ctx context.Context, args ...string) error {
	// #nosec G204 -- serviceManager is a build-time constant and args are paths
	// this process built.
	if err := exec.CommandContext(ctx, serviceManager, args...).Run(); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrRegister, serviceManager, err)
	}

	return nil
}
