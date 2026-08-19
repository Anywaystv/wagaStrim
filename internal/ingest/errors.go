// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package ingest

import "errors"

var (
	// ErrBuildAPI is returned when the WebRTC stack cannot be constructed.
	ErrBuildAPI = errors.New("cannot build webrtc api")
	// ErrUnknownKey is returned when a key matches no ingest. The caller must not
	// say anything more specific than this to the network.
	ErrUnknownKey = errors.New("unknown key")
	// ErrWrongRole is returned when a valid key was presented at the wrong endpoint.
	ErrWrongRole = errors.New("key belongs to the other endpoint")
	// ErrNotSending is returned when a WHIP offer does not actually offer media.
	ErrNotSending = errors.New("offer does not send media")
	// ErrBadOffer is returned when the SDP cannot be parsed or negotiated.
	ErrBadOffer = errors.New("cannot negotiate offer")
	// ErrAlreadyLive is returned when an ingest already has a publisher.
	ErrAlreadyLive = errors.New("ingest already has a publisher")
	// ErrNoSession is returned when a teardown names a resource that is gone.
	ErrNoSession = errors.New("no such session")
)
