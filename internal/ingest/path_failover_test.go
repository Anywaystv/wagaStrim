// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package ingest

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/stun/v3"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"
)

func TestPathFailoverSurvivesRepeatedHandoverWithoutNewSession(t *testing.T) {
	checkPathHandover(t, true)
}

func TestPathHandoverWithoutFailoverReproducesDisconnect(t *testing.T) {
	checkPathHandover(t, false)
}

//nolint:cyclop // Runs the same bounded handover sequence with and without the fix.
func checkPathHandover(t *testing.T, enabled bool) {
	t.Helper()
	failover := &pathFailover{}
	handler := failover.binding
	if !enabled {
		handler = nil
	}
	agent, err := ice.NewAgentWithOptions(
		ice.WithNetworkTypes([]ice.NetworkType{ice.NetworkTypeUDP4}),
		ice.WithMulticastDNSMode(ice.MulticastDNSModeDisabled),
		ice.WithCandidateTypes([]ice.CandidateType{ice.CandidateTypeHost}),
		ice.WithIncludeLoopback(), ice.WithIPFilter(func(ip net.IP) bool { return ip.IsLoopback() }),
		ice.WithDisconnectedTimeout(3*time.Second), ice.WithFailedTimeout(time.Second),
		ice.WithCheckInterval(50*time.Millisecond), ice.WithKeepaliveInterval(100*time.Millisecond),
		ice.WithBindingRequestHandler(handler),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, agent.Close()) })
	var selectedPort atomic.Int64
	var failed atomic.Bool
	require.NoError(t, agent.OnConnectionStateChange(func(state ice.ConnectionState) {
		if state == ice.ConnectionStateFailed {
			failed.Store(true)
		}
	}))
	require.NoError(t, agent.OnSelectedCandidatePairChange(func(local, remote ice.Candidate) {
		failover.selected.Store(&webrtc.ICECandidatePair{
			Local:  &webrtc.ICECandidate{Address: local.Address(), Port: testCandidatePort(local)},
			Remote: &webrtc.ICECandidate{Address: remote.Address(), Port: testCandidatePort(remote)},
		})
		selectedPort.Store(int64(remote.Port()))
	}))
	gathered := make(chan struct{})
	require.NoError(t, agent.OnCandidate(func(candidate ice.Candidate) {
		if candidate == nil {
			close(gathered)
		}
	}))
	require.NoError(t, agent.GatherCandidates())
	select {
	case <-gathered:
	case <-time.After(3 * time.Second):
		require.FailNow(t, "candidate gathering timed out")
	}
	candidates, err := agent.GetLocalCandidates()
	require.NoError(t, err)
	require.NotEmpty(t, candidates)
	destination := &net.UDPAddr{IP: net.ParseIP(candidates[0].Address()), Port: candidates[0].Port()}
	ufrag, password, err := agent.GetLocalUserCredentials()
	require.NoError(t, err)
	const senderUfrag = "handover-test"
	const senderPassword = "public-test-password-not-a-secret"
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	connected := make(chan error, 1)
	go func() { _, acceptErr := agent.Accept(ctx, senderUfrag, senderPassword); connected <- acceptErr }()
	paths := make([]*net.UDPConn, 2)
	for i := range paths {
		paths[i], err = net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, paths[i].Close()) })
	}
	active := 0
	start := time.Now()
	for phase := range 4 {
		if phase > 0 {
			active = 1 - active
		}
		phaseStart := time.Now()
		duration := 3 * time.Second
		if !enabled && phase == 1 {
			duration = 5 * time.Second
		}
		for time.Since(phaseStart) < duration {
			// Only the first path nominates. Subsequent paths reproduce bonded
			// keepalive probes, which deliberately omit USE-CANDIDATE.
			requestSetters := []stun.Setter{
				stun.TransactionID, stun.BindingRequest,
				stun.NewUsername(ufrag + ":" + senderUfrag), ice.AttrControlling(1), ice.PriorityAttr(1234),
			}
			if phase == 0 {
				requestSetters = append(requestSetters, ice.UseCandidate())
			}
			requestSetters = append(requestSetters, stun.NewShortTermIntegrity(password), stun.Fingerprint)
			request, buildErr := stun.Build(requestSetters...)
			require.NoError(t, buildErr)
			_, err = paths[active].WriteToUDP(request.Raw, destination)
			require.NoError(t, err)
			pumpBindingResponses(t, paths[active], senderPassword)
			if enabled {
				require.False(t, failed.Load(), "session failed during handover after %s", time.Since(start))
			}
		}
		if !enabled && phase == 1 {
			require.True(t, failed.Load(), "without failover the silent selected path must reproduce the timeout")

			return
		}
		address, ok := paths[active].LocalAddr().(*net.UDPAddr)
		require.True(t, ok)
		require.Equal(t, int64(address.Port), selectedPort.Load(), "phase %d did not select working path", phase)
	}
	select {
	case err = <-connected:
		require.NoError(t, err)
	case <-ctx.Done():
		require.FailNow(t, "session did not connect")
	}
}

func testCandidatePort(candidate ice.Candidate) uint16 {
	port := candidate.Port()
	if port < 0 || port > 65535 {
		return 0
	}

	return uint16(port)
}

func pumpBindingResponses(t *testing.T, conn *net.UDPConn, password string) {
	t.Helper()
	buffer := make([]byte, 1500)
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(50*time.Millisecond)))
	for {
		n, remote, err := conn.ReadFromUDP(buffer)
		if err != nil {
			var netErr net.Error
			require.ErrorAs(t, err, &netErr)
			require.True(t, netErr.Timeout())

			return
		}
		message := &stun.Message{Raw: append([]byte(nil), buffer[:n]...)}
		require.NoError(t, message.Decode())
		if message.Type != stun.BindingRequest {
			continue
		}
		require.NoError(t, stun.NewShortTermIntegrity(password).Check(message))
		response, err := stun.Build(message, stun.BindingSuccess,
			&stun.XORMappedAddress{IP: remote.IP, Port: remote.Port}, stun.NewShortTermIntegrity(password), stun.Fingerprint)
		require.NoError(t, err)
		_, err = conn.WriteToUDP(response.Raw, remote)
		require.NoError(t, err)
	}
}

type seenCandidate struct {
	ice.Candidate
	last time.Time
}

func (c *seenCandidate) LastReceived() time.Time { return c.last }

func TestPathFailoverKeepsHealthySelectionAndRequiresValidatedAlternative(t *testing.T) {
	const address = "127.0.0.1"
	const network = "udp"
	failover := &pathFailover{}
	local, err := ice.NewCandidateHost(&ice.CandidateHostConfig{
		Network: network, Address: address, Port: 8189, Component: 1,
	})
	require.NoError(t, err)
	remote, err := ice.NewCandidateHost(&ice.CandidateHostConfig{
		Network: network, Address: address, Port: 40000, Component: 1,
	})
	require.NoError(t, err)
	old := &seenCandidate{Candidate: remote, last: time.Now()}
	selected := &ice.CandidatePair{Local: local, Remote: old}
	other, err := ice.NewCandidateHost(&ice.CandidateHostConfig{
		Network: network, Address: address, Port: 40001, Component: 1,
	})
	require.NoError(t, err)
	alternative := &ice.CandidatePair{Local: local, Remote: other}
	failover.selected.Store(&webrtc.ICECandidatePair{
		Local:  &webrtc.ICECandidate{Address: address, Port: 8189},
		Remote: &webrtc.ICECandidate{Address: address, Port: 40000},
	})
	require.False(t, failover.binding(nil, local, other, alternative), "unknown selection must not move")
	require.False(t, failover.binding(nil, local, remote, selected))
	alternative.UpdateRoundTripTime(time.Millisecond)
	require.False(t, failover.binding(nil, local, other, alternative), "healthy selection must not flap")
	old.last = time.Now().Add(-pathSilence - time.Second)
	require.True(t, failover.binding(nil, local, other, alternative), "validated path must replace silent selection")
	unvalidated := &ice.CandidatePair{Local: local, Remote: other}
	require.False(t, failover.binding(nil, local, other, unvalidated), "unvalidated path must not win")
	failover.selected.Store(nil)
	require.False(t, failover.binding(nil, local, other, alternative))
}
