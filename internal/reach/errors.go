// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package reach

import "errors"

var (
	// ErrNoSTUN is returned when no STUN server answered.
	ErrNoSTUN = errors.New("no STUN server answered")
	// ErrNoAddress is returned when a STUN server answered without an address.
	ErrNoAddress = errors.New("STUN reply carried no address")
)
