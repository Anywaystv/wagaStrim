// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package egress

import "errors"

var (
	ErrCapacity = errors.New("subscriber limit reached")
	// ErrUnknownKey is returned when a key matches no ingest.
	ErrUnknownKey = errors.New("unknown key")
	// ErrWrongRole is returned when a sender key was presented to a receiver endpoint.
	ErrWrongRole = errors.New("key belongs to the other endpoint")
	// ErrNotReceiving is returned when a WHEP offer wants to send media.
	ErrNotReceiving = errors.New("offer does not receive media")
	// ErrOffline is returned when the ingest has no publisher yet.
	ErrOffline = errors.New("nothing is publishing to that camera")
	// ErrBadOffer is returned when the SDP cannot be parsed or negotiated.
	ErrBadOffer = errors.New("cannot negotiate offer")
	// ErrNoSession is returned when a teardown names a resource that is gone.
	ErrNoSession = errors.New("no such session")
)
