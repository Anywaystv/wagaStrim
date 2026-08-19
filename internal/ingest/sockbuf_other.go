// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

//go:build !darwin && !linux

package ingest

// ProbeSocketBuffer reports zero on platforms where the granted size is not
// read back. Zero means not measured, not a buffer of no size.
func ProbeSocketBuffer(_ int) (int, error) {
	return 0, nil
}
