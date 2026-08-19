// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package signal

import "errors"

// ErrServe is returned when the public listener stops for any reason but shutdown.
var ErrServe = errors.New("signaling listener failed")
