// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package ingest

import (
	"testing"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stamped writes one packet through the interceptor and returns the header the
// subscriber would have received.
func stamped(t *testing.T, negotiated uint8) *rtp.Header {
	t.Helper()

	stamper, err := playoutDelayFactory{}.NewInterceptor("")
	require.NoError(t, err)

	info := &interceptor.StreamInfo{}
	if negotiated != 0 {
		info.RTPHeaderExtensions = []interceptor.RTPHeaderExtension{
			{URI: playoutDelayURI, ID: int(negotiated)},
		}
	}

	sent := make(chan *rtp.Header, 1)
	writer := stamper.BindLocalStream(info, interceptor.RTPWriterFunc(
		func(header *rtp.Header, _ []byte, _ interceptor.Attributes) (int, error) {
			sent <- header

			return 0, nil
		}))

	_, err = writer.Write(&rtp.Header{}, nil, nil)
	require.NoError(t, err)

	return <-sent
}

func TestOutgoingVideoAsksTheReceiverToHold(t *testing.T) {
	header := stamped(t, 5)

	var hold rtp.PlayoutDelayExtension
	require.NoError(t, hold.Unmarshal(header.GetExtension(5)))

	assert.Equal(t, uint16(playoutFloor), hold.MinDelay, "300ms, the same the player page asks for")
	assert.Equal(t, uint16(playoutCeiling), hold.MaxDelay)
}

// A receiver that did not negotiate the extension must not be sent one: the id
// would name whatever that session put there instead.
func TestAStreamWithoutTheExtensionIsLeftAlone(t *testing.T) {
	header := stamped(t, 0)

	assert.False(t, header.Extension)
	assert.Empty(t, header.Extensions)
}
