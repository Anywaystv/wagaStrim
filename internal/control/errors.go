// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package control

import "errors"

// ErrBadRequest is returned when a body is not the document this endpoint takes.
var ErrBadRequest = errors.New("cannot read request")
