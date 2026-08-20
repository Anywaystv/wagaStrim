// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package ingest

import (
	"fmt"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
)

// playoutDelayURI names the extension a receiver reads to decide how much media
// to hold before playing any of it.
const playoutDelayURI = "http://www.webrtc.org/experiments/rtp-hdrext/playout-delay"

// The hold asked of a subscriber, in the extension's own units of 10ms. A
// WebRTC receiver otherwise keeps the smallest buffer that keeps up, which is
// right for a conversation and wrong here: nobody is talking back, and a frame
// arrives as a clump of packets. At the edge of that jitter the decoder stutters
// on every late packet, which is what reaches Twitch or Kick as a re-encode of
// a stuttering picture.
//
// The player page asks for the same 300ms through jitterBufferTarget. That is
// the same request through a JavaScript API, and only newer builds have it: the
// name changed from playoutDelayHint, and an OBS Browser Source is whatever
// Chromium its CEF was cut from. The extension reaches the ones that do not,
// and it applies from the first packet rather than from whenever the page runs.
//
// The ceiling is loose on purpose. Pinning it to the floor would stop a receiver
// holding more when a link genuinely needs it, and the queue that grows is the
// one this relay already watches.
const (
	playoutFloor   = 30
	playoutCeiling = 100
)

// playoutDelayFactory builds one playoutDelay per PeerConnection.
type playoutDelayFactory struct{}

// NewInterceptor implements interceptor.Factory.
func (playoutDelayFactory) NewInterceptor(string) (interceptor.Interceptor, error) {
	payload, err := (&rtp.PlayoutDelayExtension{
		MinDelay: playoutFloor,
		MaxDelay: playoutCeiling,
	}).Marshal()
	if err != nil {
		return nil, fmt.Errorf("%w: playout delay: %w", ErrBuildAPI, err)
	}

	return &playoutDelay{payload: payload}, nil
}

// playoutDelay stamps every outgoing video packet with the hold above.
type playoutDelay struct {
	interceptor.NoOp

	payload []byte
}

// BindLocalStream adds the extension to a stream that negotiated it. A stream
// that did not is left alone: an id of zero is not a valid extension id.
func (p *playoutDelay) BindLocalStream(
	info *interceptor.StreamInfo,
	writer interceptor.RTPWriter,
) interceptor.RTPWriter {
	var id uint8

	for _, extension := range info.RTPHeaderExtensions {
		if extension.URI == playoutDelayURI {
			id = uint8(extension.ID) //nolint:gosec // negotiated ids are 1 to 14.

			break
		}
	}

	if id == 0 {
		return writer
	}

	return interceptor.RTPWriterFunc(
		func(header *rtp.Header, payload []byte, attributes interceptor.Attributes) (int, error) {
			if err := header.SetExtension(id, p.payload); err != nil {
				return 0, fmt.Errorf("%w: playout delay: %w", ErrBuildAPI, err)
			}

			return writer.Write(header, payload, attributes)
		},
	)
}
