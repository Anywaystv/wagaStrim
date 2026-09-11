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

// Playout bounds in 10ms units. The 300ms floor matches the player's
// jitterBufferTarget and also reaches clients without that JavaScript API.
// The higher ceiling lets receivers absorb additional jitter.
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
