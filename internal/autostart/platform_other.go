// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

//go:build !darwin

package autostart

func hasPlist() bool { return false }

func plistPath() (string, error) { return "", ErrRegister }
