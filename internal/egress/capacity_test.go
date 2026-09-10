// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package egress

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSubscriberCapacityIncludesPendingNegotiations(t *testing.T) {
	server := &Server{pending: make(map[*Session]bool), sessions: make(map[string]*Session)}
	for range maxSubscribersPerIngest {
		require.NotNil(t, server.reserve("camera"))
	}
	require.Nil(t, server.reserve("camera"))
	var others []*Session
	for idx := range maxSubscribers - maxSubscribersPerIngest {
		session := server.reserve(fmt.Sprint(idx))
		require.NotNil(t, session)
		others = append(others, session)
	}
	require.Nil(t, server.reserve("new-camera"))
	for _, session := range others {
		server.releaseReservation(session)
	}
	server.sessions["viewer"] = &Session{IngestID: "second"}
	for range maxSubscribersPerIngest - 1 {
		require.NotNil(t, server.reserve("second"))
	}
	require.Nil(t, server.reserve("second"))
}

func TestPendingCancellationIsScopedAndCounted(t *testing.T) {
	server := &Server{pending: make(map[*Session]bool), sessions: make(map[string]*Session)}
	old := server.reserve("camera")
	other := server.reserve("other")
	server.CloseIngest("camera")
	require.False(t, server.pending[old])
	require.True(t, server.pending[other])
	for range maxSubscribersPerIngest - 1 {
		require.NotNil(t, server.reserve("camera"))
	}
	require.Nil(t, server.reserve("camera"), "canceled work still occupies a slot until it exits")
	server.releaseReservation(old)
	fresh := server.reserve("camera")
	require.NotNil(t, fresh)
	require.True(t, server.pending[fresh])
	server.Close()
	require.False(t, server.pending[fresh])
	require.False(t, server.pending[other])
	require.Nil(t, server.reserve("new-camera"), "shutdown rejects new work")
}
