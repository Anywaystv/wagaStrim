// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package ingest

import (
	"testing"

	"github.com/pion/ice/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecoveryRemovalIsScopedAcrossPathsAndAddressReuse(t *testing.T) {
	wifi, sender := recoverySocketPair(t)
	cellular, other := recoverySocketPair(t)
	cellular.recoveryState = wifi.recoveryState
	wifi.conns = []*recoveryConn{wifi, cellular}
	underlying := ice.NewMultiUDPMuxDefault(
		ice.NewUDPMuxDefault(ice.UDPMuxParams{UDPConn: wifi}),
		ice.NewUDPMuxDefault(ice.UDPMuxParams{UDPConn: cellular}),
	)
	mux := newRecoveryMux(underlying, wifi.recoveryState)
	t.Cleanup(func() { require.NoError(t, mux.Close()) })
	for _, ufrag := range []string{"old", "old-other"} {
		_, err := mux.GetConn(ufrag, wifi.LocalAddr())
		require.NoError(t, err)
	}
	bindRecoveryPath(t, wifi, sender.LocalAddr(), "old:phone", true)
	bindRecoveryPath(t, cellular, other.LocalAddr(), "old:phone", true)
	assert.Equal(t, "ice:old:phone", wifi.authenticatedIdentity(sender.LocalAddr()))
	assert.Equal(t, "ice:old:phone", cellular.authenticatedIdentity(other.LocalAddr()))
	// A replacement has already authenticated on one of the old addresses.
	bindRecoveryPath(t, wifi, sender.LocalAddr(), "old-other:phone", true)
	bindRecoveryPath(t, cellular, other.LocalAddr(), "old:pending", false)
	wifi.mu.Lock()
	wifi.deliveries = map[string]*deliveryBatch{}
	for _, identity := range []string{"ice:old:phone", "ice:old-other:phone"} {
		id := recoveryPacketID{remote: identity, ssrc: 1, sequenceNumber: 1}
		wifi.storePacketLocked(id, []byte{1})
		wifi.streams[recoveryStreamID{remote: identity, ssrc: 1}] = &recoveryStream{}
		wifi.storeGroupLocked(recoveryGroupID{remote: identity, ssrc: 1}, recoveryGroup{})
		wifi.ready = append(wifi.ready, recoveredDatagram{identity: identity})
	}
	wifi.deliveries["old"] = &deliveryBatch{identity: "ice:old:phone"}
	wifi.deliveries["new"] = &deliveryBatch{identity: "ice:old-other:phone"}
	wifi.mu.Unlock()

	mux.RemoveConnByUfrag("old")
	mux.RemoveConnByUfrag("old")
	assert.Empty(t, cellular.authenticatedIdentity(other.LocalAddr()))
	assert.Equal(t, "ice:old-other:phone", wifi.authenticatedIdentity(sender.LocalAddr()))
	wifi.mu.Lock()
	assert.Empty(t, cellular.pendingBindings)
	assert.Len(t, wifi.packets, 1)
	assert.Len(t, wifi.packetOrder, 1)
	assert.Len(t, wifi.streams, 1)
	assert.Len(t, wifi.groups, 1)
	assert.Len(t, wifi.groupOrder, 1)
	assert.Len(t, wifi.ready, 1)
	assert.Len(t, wifi.deliveries, 1)
	assert.True(t, wifi.active["old-other"])
	wifi.mu.Unlock()

	// Neither a late success nor in-flight recovery work may resurrect state.
	bindRecoveryPath(t, cellular, other.LocalAddr(), "old:phone", true)
	assert.Empty(t, cellular.authenticatedIdentity(other.LocalAddr()))
	wifi.remember(recoveryPacketID{remote: "ice:old:phone", ssrc: 1}, testRecoveryRTP(2, 1), sender.LocalAddr())
	packets := make([][]byte, 8)
	for i := range packets {
		packets[i] = testRecoveryRTP(uint16(i), 1)
	}
	cellular.consumeParity(testRecoveryParity(packets), other.LocalAddr())
	wifi.mu.Lock()
	assert.Len(t, wifi.packets, 1)
	assert.Len(t, wifi.streams, 1)
	wifi.mu.Unlock()

	_, err := mux.GetConn("fresh", cellular.LocalAddr())
	require.NoError(t, err)
	bindRecoveryPath(t, cellular, other.LocalAddr(), "fresh:phone", true)
	assert.Equal(t, "ice:fresh:phone", cellular.authenticatedIdentity(other.LocalAddr()))
}
