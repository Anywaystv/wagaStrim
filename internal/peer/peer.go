// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

// Package peer holds the SDP exchange shared by WHIP and WHEP. Publishing and
// subscribing differ in what they attach to the connection, not in how the
// offer and answer are traded, and this was the same twenty lines in both.
package peer

import (
	"errors"
	"fmt"

	"github.com/pion/logging"
	"github.com/pion/webrtc/v4"
)

// ErrNegotiate is returned when an offer cannot be turned into an answer.
var ErrNegotiate = errors.New("cannot negotiate offer")

// Answer applies an offer and returns a fully gathered answer. Neither endpoint
// trickles, so gathering completes before the answer goes back.
func Answer(conn *webrtc.PeerConnection, offer webrtc.SessionDescription) (string, error) {
	if err := conn.SetRemoteDescription(offer); err != nil {
		return "", fmt.Errorf("%w: remote description: %w", ErrNegotiate, err)
	}

	answer, err := conn.CreateAnswer(nil)
	if err != nil {
		return "", fmt.Errorf("%w: create answer: %w", ErrNegotiate, err)
	}

	gathered := webrtc.GatheringCompletePromise(conn)

	if err := conn.SetLocalDescription(answer); err != nil {
		return "", fmt.Errorf("%w: local description: %w", ErrNegotiate, err)
	}

	<-gathered

	return conn.LocalDescription().SDP, nil
}

// Discard closes a half-built connection and returns why it was abandoned, so a
// failed negotiation never leaves a PeerConnection and its ICE agent running.
func Discard(conn *webrtc.PeerConnection, cause error, log logging.LeveledLogger) error {
	if err := conn.Close(); err != nil {
		log.Warnf("close abandoned peer: %v", err)
	}

	return cause
}

// Direction reports whether an offer carries any media in the wanted direction.
// A WHIP client pointed at a WHEP endpoint, or the reverse, produces an offer
// facing the wrong way, and catching it here turns a black source into a
// diagnosis.
func Direction(offer webrtc.SessionDescription, wanted string) (bool, error) {
	parsed, err := offer.Unmarshal()
	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrNegotiate, err)
	}

	for _, media := range parsed.MediaDescriptions {
		for _, attr := range media.Attributes {
			if attr.Key == wanted || attr.Key == "sendrecv" {
				return true, nil
			}
		}
	}

	return false, nil
}
