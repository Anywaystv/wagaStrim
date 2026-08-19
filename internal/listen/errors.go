// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package listen

import "errors"

// ErrServe is returned when a listener stops for any reason but shutdown.
var ErrServe = errors.New("listener failed")
