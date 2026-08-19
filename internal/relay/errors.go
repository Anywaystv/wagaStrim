// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package relay

import "errors"

var (
	// ErrBuildTrack is returned when an outbound track cannot be created.
	ErrBuildTrack = errors.New("cannot build track")
	// ErrNotPublishing is returned when nobody is sending to that ingest.
	ErrNotPublishing = errors.New("ingest has no publisher")
)
