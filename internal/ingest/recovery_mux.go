// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package ingest

import (
	"maps"
	"net"
	"slices"
	"strings"

	"github.com/pion/ice/v4"
)

type recoveryMux struct {
	ice.UDPMux
	state *recoveryState
}

func newRecoveryMux(mux ice.UDPMux, state *recoveryState) *recoveryMux {
	state.mu.Lock()
	state.active = make(map[string]bool)
	state.mu.Unlock()

	return &recoveryMux{UDPMux: mux, state: state}
}

func (m *recoveryMux) GetConn(ufrag string, addr net.Addr) (net.PacketConn, error) {
	m.state.mu.Lock()
	defer m.state.mu.Unlock()
	conn, err := m.UDPMux.GetConn(ufrag, addr)
	if err == nil {
		m.state.active[ufrag] = true
	}

	return conn, err
}

func (m *recoveryMux) RemoveConnByUfrag(ufrag string) {
	state := m.state
	state.mu.Lock()
	delete(state.active, ufrag)
	prefix := "ice:" + ufrag + ":"
	for _, conn := range state.conns {
		conn.ready = slices.DeleteFunc(conn.ready, func(datagram recoveredDatagram) bool {
			return strings.HasPrefix(datagram.identity, prefix)
		})
		maps.DeleteFunc(conn.identities, func(_ string, username string) bool {
			return strings.HasPrefix(username, ufrag+":")
		})
		maps.DeleteFunc(conn.pendingBindings, func(_ recoveryBindingID, username string) bool {
			return strings.HasPrefix(username, ufrag+":")
		})
		maps.DeleteFunc(conn.deliveries, func(_ string, batch *deliveryBatch) bool {
			return strings.HasPrefix(batch.identity, prefix)
		})
	}
	maps.DeleteFunc(state.packets, func(id recoveryPacketID, _ []byte) bool {
		return strings.HasPrefix(id.remote, prefix)
	})
	state.packetOrder = slices.DeleteFunc(state.packetOrder[state.packetHead:], func(id recoveryPacketID) bool {
		return strings.HasPrefix(id.remote, prefix)
	})
	state.packetHead = 0
	maps.DeleteFunc(state.streams, func(id recoveryStreamID, _ *recoveryStream) bool {
		return strings.HasPrefix(id.remote, prefix)
	})
	maps.DeleteFunc(state.groups, func(id recoveryGroupID, _ recoveryGroup) bool {
		return strings.HasPrefix(id.remote, prefix)
	})
	state.groupOrder = slices.DeleteFunc(state.groupOrder[state.groupHead:], func(id recoveryGroupID) bool {
		return strings.HasPrefix(id.remote, prefix)
	})
	state.groupHead = 0
	state.mu.Unlock()
	m.UDPMux.RemoveConnByUfrag(ufrag)
}
