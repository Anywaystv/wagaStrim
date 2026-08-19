// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package ui

import "errors"

var (
	// ErrParseTemplate is returned when the embedded page fails to compile.
	ErrParseTemplate = errors.New("cannot parse template")
	// ErrEmbeddedAssets is returned when the embedded static directory cannot be opened.
	ErrEmbeddedAssets = errors.New("cannot open embedded assets")
	// ErrBadRequest is returned when a request body is not the shape the handler expects.
	ErrBadRequest = errors.New("bad request")
)
