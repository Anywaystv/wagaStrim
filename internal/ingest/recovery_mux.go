// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package ingest

import (
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
	s := m.state
	s.mu.Lock()
	delete(s.active, ufrag)
	prefix := "ice:" + ufrag + ":"
	for _, conn := range s.conns {
		conn.ready = slices.DeleteFunc(conn.ready, func(datagram recoveredDatagram) bool {
			return strings.HasPrefix(datagram.identity, prefix)
		})
		for remote, username := range conn.identities {
			if strings.HasPrefix(username, ufrag+":") {
				delete(conn.identities, remote)
			}
		}
		for key, username := range conn.pendingBindings {
			if strings.HasPrefix(username, ufrag+":") {
				delete(conn.pendingBindings, key)
			}
		}
		for remote, batch := range conn.deliveries {
			if strings.HasPrefix(batch.identity, prefix) {
				delete(conn.deliveries, remote)
			}
		}
	}
	for id := range s.packets {
		if strings.HasPrefix(id.remote, prefix) {
			delete(s.packets, id)
		}
	}
	s.packetOrder = slices.DeleteFunc(s.packetOrder[s.packetHead:], func(id recoveryPacketID) bool {
		return strings.HasPrefix(id.remote, prefix)
	})
	s.packetHead = 0
	for id := range s.streams {
		if strings.HasPrefix(id.remote, prefix) {
			delete(s.streams, id)
		}
	}
	for id := range s.groups {
		if strings.HasPrefix(id.remote, prefix) {
			delete(s.groups, id)
		}
	}
	s.groupOrder = slices.DeleteFunc(s.groupOrder[s.groupHead:], func(id recoveryGroupID) bool {
		return strings.HasPrefix(id.remote, prefix)
	})
	s.groupHead = 0
	s.mu.Unlock()
	m.UDPMux.RemoveConnByUfrag(ufrag)
}
