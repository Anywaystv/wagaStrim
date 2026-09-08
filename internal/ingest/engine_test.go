// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package ingest

import (
	"testing"

	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSettingEngineRejectsInvalidPublicIP(t *testing.T) {
	engine, mux, err := NewSettingEngine(0, "not-an-ip")

	assert.Nil(t, engine)
	assert.Nil(t, mux)
	assert.ErrorContains(t, err, "invalid public ICE IP mapping")
}

func TestSettingEngineAdvertisesPublicIP(t *testing.T) {
	engine, mux, err := NewSettingEngine(0, "203.0.113.10/127.0.0.1")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mux.Close()) })

	peer, err := webrtc.NewAPI(webrtc.WithSettingEngine(*engine)).NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, peer.Close()) })
	_, err = peer.CreateDataChannel("candidate-probe", nil)
	require.NoError(t, err)

	offer, err := peer.CreateOffer(nil)
	require.NoError(t, err)
	gathered := webrtc.GatheringCompletePromise(peer)
	require.NoError(t, peer.SetLocalDescription(offer))
	<-gathered

	require.NotNil(t, peer.LocalDescription())
	assert.Contains(t, peer.LocalDescription().SDP, "203.0.113.10")
	assert.Contains(t, peer.LocalDescription().SDP, "127.0.0.1")
	assert.Contains(t, peer.LocalDescription().SDP, "typ srflx")
}
