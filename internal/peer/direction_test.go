// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package peer

import (
	"strings"
	"testing"

	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// offerWith builds the smallest offer carrying one video section, with the
// given attribute lines appended to it.
func offerWith(lines ...string) webrtc.SessionDescription {
	var sdp strings.Builder

	sdp.WriteString("v=0\r\n" +
		"o=- 0 0 IN IP4 127.0.0.1\r\n" +
		"s=-\r\n" +
		"t=0 0\r\n" +
		"m=video 9 UDP/TLS/RTP/SAVPF 96\r\n" +
		"c=IN IP4 0.0.0.0\r\n")

	for _, line := range lines {
		sdp.WriteString(line + "\r\n")
	}

	return webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdp.String()}
}

// A publisher that leaves the direction out is offering sendrecv, and rejecting
// it was what stopped Moblin from connecting.
func TestAMissingDirectionIsSendrecv(t *testing.T) {
	sends, err := Direction(offerWith("a=mid:0"), "sendonly")
	require.NoError(t, err)
	assert.True(t, sends, "no direction attribute means sendrecv, which sends")

	receives, err := Direction(offerWith("a=mid:0"), "recvonly")
	require.NoError(t, err)
	assert.True(t, receives, "the same section also receives")
}

func TestASpelledOutDirectionStillDecides(t *testing.T) {
	sends, err := Direction(offerWith("a=sendonly"), "sendonly")
	require.NoError(t, err)
	assert.True(t, sends)

	sends, err = Direction(offerWith("a=recvonly"), "sendonly")
	require.NoError(t, err)
	assert.False(t, sends, "a receiver pointed at the publish endpoint must be told so")

	sends, err = Direction(offerWith("a=inactive"), "sendonly")
	require.NoError(t, err)
	assert.False(t, sends, "inactive carries nothing in either direction")
}

func TestAnOfferWithNoMediaSendsNothing(t *testing.T) {
	sends, err := Direction(webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  "v=0\r\no=- 0 0 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\n",
	}, "sendonly")
	require.NoError(t, err)
	assert.False(t, sends)
}
