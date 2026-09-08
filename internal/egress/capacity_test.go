// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package egress

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSubscriberCapacityIncludesPendingNegotiations(t *testing.T) {
	s := &Server{pending: make(map[string]int), sessions: make(map[string]*Session)}
	for range maxSubscribersPerIngest {
		require.True(t, s.reserve("camera"))
	}
	require.False(t, s.reserve("camera"))
	s.pending["other"] = maxSubscribers - maxSubscribersPerIngest
	require.False(t, s.reserve("new-camera"))
	delete(s.pending, "other")
	s.sessions["viewer"] = &Session{IngestID: "second"}
	s.pending["second"] = maxSubscribersPerIngest - 1
	require.False(t, s.reserve("second"))
}
