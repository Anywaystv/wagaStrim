// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package ingest

import (
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/logging"
	"github.com/pion/stun/v3"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

func (c *recoveryConn) identity(remote net.Addr) string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.identityLocked(remote)
}

func TestRecoveryReadyReleasesDeliveredPackets(t *testing.T) {
	conn := &recoveryConn{recoveryState: newRecoveryState()}
	first := recoveredDatagram{data: []byte{1}, identity: "first"}
	second := recoveredDatagram{data: []byte{2}, identity: "second"}
	conn.ready = []recoveredDatagram{first, second}
	backing := conn.ready
	for _, want := range []recoveredDatagram{first, second} {
		got, ok := conn.popReady()
		require.True(t, ok)
		assert.Equal(t, want, got)
		assert.Zero(t, backing[len(conn.ready)])
	}
	_, ok := conn.popReady()
	assert.False(t, ok)
}

func TestRecoveryIdentityRequiresPionAuthenticatedSuccess(t *testing.T) {
	conn, sender := recoverySocketPair(t)
	mux := ice.NewUDPMuxDefault(ice.UDPMuxParams{UDPConn: conn})
	t.Cleanup(func() { require.NoError(t, mux.Close()) })
	engine := webrtc.SettingEngine{}
	engine.SetIncludeLoopbackCandidate(true)
	conn.conns = append(conn.conns, conn)
	engine.SetICEUDPMux(newRecoveryMux(mux, conn.recoveryState))
	api, err := buildAPI(&engine, []string{"h264"}, nil)
	require.NoError(t, err)
	receiver, err := api.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, receiver.Close()) })
	phone, _ := publisher(t)
	// Read credentials before SetRemoteDescription starts ICE asynchronously.
	local, err := receiver.SCTP().Transport().ICETransport().GetLocalParameters()
	require.NoError(t, err)
	require.NoError(t, receiver.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer, SDP: offerFrom(t, phone),
	}))
	answer, err := receiver.CreateAnswer(nil)
	require.NoError(t, err)
	gathered := webrtc.GatheringCompletePromise(receiver)
	require.NoError(t, receiver.SetLocalDescription(answer))
	<-gathered
	remote, err := phone.SCTP().Transport().ICETransport().GetLocalParameters()
	require.NoError(t, err)
	username := local.UsernameFragment + ":" + remote.UsernameFragment
	for _, password := range []string{"wrong-password", local.Password} {
		request, err := stun.Build(stun.TransactionID, stun.BindingRequest,
			stun.NewUsername(username), stun.NewShortTermIntegrity(password), stun.Fingerprint)
		require.NoError(t, err)
		_, err = sender.Write(request.Raw)
		require.NoError(t, err)
		require.NoError(t, sender.SetReadDeadline(time.Now().Add(100*time.Millisecond)))
		_, err = sender.Read(make([]byte, 1500))
		if password == local.Password {
			require.NoError(t, err)
			assert.Equal(t, "ice:"+username, conn.identity(sender.LocalAddr()))
		} else {
			require.Error(t, err)
			assert.False(t, strings.HasPrefix(conn.identity(sender.LocalAddr()), "ice:"))
		}
	}
	require.NoError(t, receiver.Close())
	assert.Empty(t, conn.authenticatedIdentity(sender.LocalAddr()), "closing ICE must revoke recovery access")
}

func TestRecoveryJoinsAuthenticatedPathsAcrossSockets(t *testing.T) {
	first, wifi := recoverySocketPair(t)
	second, cellular := recoverySocketPair(t)
	second.recoveryState = first.recoveryState
	bindRecoveryPath(t, first, wifi.LocalAddr(), "receiver:phone", true)
	bindRecoveryPath(t, second, cellular.LocalAddr(), "receiver:phone", true)
	packets := make([][]byte, 8)
	for index := range packets {
		packets[index] = testRecoveryRTP(700+uint16(index), byte(index+1))
		if index == 2 || index == 5 {
			continue
		}
		conn, sender := first, wifi
		if index%2 == 1 {
			conn, sender = second, cellular
		}
		_, err := sender.Write(packets[index])
		require.NoError(t, err)
		assert.Equal(t, packets[index], readRecoveryPacket(t, conn))
	}
	_, rebuilt := first.consumeParity(testRecoveryParity(packets), wifi.LocalAddr())
	assert.False(t, rebuilt)
	_, err := cellular.Write(packets[2])
	require.NoError(t, err)
	assert.Equal(t, packets[2], readRecoveryPacket(t, second))
	assert.Equal(t, packets[5], readRecoveryPacket(t, second))
	assert.Empty(t, first.groups)
	assert.Empty(t, first.streams[recoveryStreamID{remote: "ice:receiver:phone", ssrc: 0x01020304}].missing)
}

func TestRecoveryUsernameAloneDoesNotJoinPaths(t *testing.T) {
	first, wifi := recoverySocketPair(t)
	second, cellular := recoverySocketPair(t)
	second.recoveryState = first.recoveryState
	bindRecoveryPath(t, first, wifi.LocalAddr(), "receiver:phone", true)
	bindRecoveryPath(t, second, cellular.LocalAddr(), "receiver:phone", false)
	assert.NotEqual(t, first.identity(wifi.LocalAddr()), second.identity(cellular.LocalAddr()))
	bindRecoveryPath(t, second, cellular.LocalAddr(), "receiver:other-phone", true)
	assert.NotEqual(t, first.identity(wifi.LocalAddr()), second.identity(cellular.LocalAddr()))
}

func bindRecoveryPath(t *testing.T, conn *recoveryConn, remote net.Addr, username string, success bool) {
	t.Helper()
	request, err := stun.Build(stun.TransactionID, stun.BindingRequest, stun.NewUsername(username))
	require.NoError(t, err)
	conn.observeBinding(request.Raw, remote, false)
	if success {
		response, err := stun.Build(stun.NewTransactionIDSetter(request.TransactionID), stun.BindingSuccess)
		require.NoError(t, err)
		// Represents the success Pion emits after authenticating the request.
		_, err = conn.WriteTo(response.Raw, remote)
		require.NoError(t, err)
	}
}

func TestRecoveryCompletedGroupsKeepOrderBounded(t *testing.T) {
	conn, sender := recoverySocketPair(t)
	// Exercise compaction independently of the wall-clock work budget.
	// TestRecoveryBudgetBypassesHistoryWithoutDroppingMedia covers that limit.
	conn.work = rate.NewLimiter(rate.Inf, 65536)
	for cycle := range 2000 {
		packets := make([][]byte, 8)
		for index := range packets {
			packets[index] = testRecoveryRTP(uint16(cycle*8+index), byte(index))
			if index == 2 || index == 5 {
				continue
			}
			id, ok := recoveryRTPPacketID(packets[index])
			require.True(t, ok)
			id.remote = conn.identity(sender.LocalAddr())
			conn.remember(id, packets[index], sender.LocalAddr())
		}
		conn.consumeParity(testRecoveryParity(packets), sender.LocalAddr())
		id, ok := recoveryRTPPacketID(packets[2])
		require.True(t, ok)
		id.remote = conn.identity(sender.LocalAddr())
		conn.remember(id, packets[2], sender.LocalAddr())
		datagram, ok := conn.popReady()
		require.True(t, ok)
		assert.Equal(t, packets[5], datagram.data)
		require.Empty(t, conn.groups)
		require.LessOrEqual(t, len(conn.groupOrder), 2*recoveryGroupLimit)
	}
}

func TestRecoveryParityRebuildsOneMissingPacket(t *testing.T) {
	receiver, sender := authenticatedRecoverySocketPair(t)
	packets := make([][]byte, 8)
	for index := range packets {
		packets[index] = testRecoveryRTP(200+uint16(index), byte(index+1))
		if index != 3 {
			_, err := sender.Write(packets[index])
			require.NoError(t, err)
		}
	}

	for range 7 {
		readRecoveryPacket(t, receiver)
	}
	_, err := sender.Write(testRecoveryParity(packets))
	require.NoError(t, err)

	rebuilt := readRecoveryPacket(t, receiver)
	assert.Equal(t, packets[3], rebuilt)
}

func TestRecoveryTracksBurstLossWithinHistory(t *testing.T) {
	for _, start := range []uint16{100, 65500} {
		conn, sender := recoverySocketPair(t)
		remote := conn.identity(sender.LocalAddr())
		stream := &recoveryStream{missing: map[uint16]time.Time{}, enabled: true}
		for _, offset := range []uint16{0, 2, 102} {
			conn.trackGapLocked(recoveryPacketID{remote: remote, ssrc: 1, sequenceNumber: start + offset}, stream)
		}
		require.Len(t, stream.missing, 100)
		assert.Contains(t, stream.missing, start+1)
		assert.Contains(t, stream.missing, start+101)
		conn.trackGapLocked(recoveryPacketID{remote: remote, ssrc: 1, sequenceNumber: start + 10000}, stream)
		require.Len(t, stream.missing, recoveryHistorySize-1)
		assert.NotContains(t, stream.missing, start+1)
		assert.Contains(t, stream.missing, start+9999)
	}
}

func TestRecoveryRequestsEveryMissingPacket(t *testing.T) {
	receiver, sender := authenticatedRecoverySocketPair(t)
	packets := make([][]byte, 8)
	for index := range packets {
		packets[index] = testRecoveryRTP(300+uint16(index), byte(index+1))
		if index != 2 && index != 5 {
			_, err := sender.Write(packets[index])
			require.NoError(t, err)
		}
	}

	for range 6 {
		readRecoveryPacket(t, receiver)
	}
	_, err := sender.Write(testRecoveryParity(packets))
	require.NoError(t, err)
	require.NoError(t, receiver.SetReadDeadline(time.Now().Add(50*time.Millisecond)))
	_, _, err = receiver.ReadFrom(make([]byte, 1500))
	assert.Error(t, err)

	request := make([]byte, 1500)
	require.NoError(t, sender.SetReadDeadline(time.Now().Add(time.Second)))
	read, err := sender.Read(request)
	require.NoError(t, err)
	require.GreaterOrEqual(t, read, recoveryHeaderSize+4)
	assert.Equal(t, "WGR1", string(request[:4]))
	assert.Equal(t, byte(recoveryRequestType), request[4])
	assert.Equal(t, byte(2), request[5])
	requested := []uint16{
		binary.BigEndian.Uint16(request[12:14]),
		binary.BigEndian.Uint16(request[14:16]),
	}
	assert.ElementsMatch(t, []uint16{302, 305}, requested)
}

func TestRecoveryCompletesPendingGroupWhenOneRepairArrives(t *testing.T) {
	receiver, sender := authenticatedRecoverySocketPair(t)
	packets := make([][]byte, 8)
	for index := range packets {
		packets[index] = testRecoveryRTP(400+uint16(index), byte(index+1))
		if index != 2 && index != 5 {
			_, err := sender.Write(packets[index])
			require.NoError(t, err)
		}
	}

	for range 6 {
		readRecoveryPacket(t, receiver)
	}
	_, err := sender.Write(testRecoveryParity(packets))
	require.NoError(t, err)
	require.NoError(t, receiver.SetReadDeadline(time.Now().Add(50*time.Millisecond)))
	_, _, err = receiver.ReadFrom(make([]byte, 1500))
	assert.Error(t, err)

	request := make([]byte, 1500)
	require.NoError(t, sender.SetReadDeadline(time.Now().Add(time.Second)))
	_, err = sender.Read(request)
	require.NoError(t, err)

	_, err = sender.Write(packets[2])
	require.NoError(t, err)
	assert.Equal(t, packets[2], readRecoveryPacket(t, receiver))
	assert.Equal(t, packets[5], readRecoveryPacket(t, receiver))
}

func TestRecoveryRejectsMalformedParity(t *testing.T) {
	receiver, sender := authenticatedRecoverySocketPair(t)
	malformed := []byte{'W', 'G', 'R', '1', recoveryParityType, 32, 0, 0, 0, 0, 0, 1}
	_, err := sender.Write(malformed)
	require.NoError(t, err)

	require.NoError(t, receiver.SetReadDeadline(time.Now().Add(50*time.Millisecond)))
	_, _, err = receiver.ReadFrom(make([]byte, 1500))
	assert.Error(t, err)
}

func TestRecoveryLengthMismatchKeepsMuxOpen(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(map[bool]string{false: "immediate", true: "pending"}[pending], func(t *testing.T) {
			conn, sender := authenticatedRecoverySocketPair(t)
			mux := ice.NewUDPMuxDefault(ice.UDPMuxParams{UDPConn: conn})
			t.Cleanup(func() { require.NoError(t, mux.Close()) })
			probe, err := mux.GetConn("probe", conn.LocalAddr())
			require.NoError(t, err)

			packets := make([][]byte, 8)
			for index := range packets {
				packets[index] = testRecoveryRTP(800+uint16(index), byte(index))
				if index == 7 || pending && index == 6 {
					continue
				}
				_, err = sender.Write(packets[index])
				require.NoError(t, err)
			}
			parity := testRecoveryParity(packets)
			// The datagram is structurally valid, but contradicts a known packet.
			binary.BigEndian.PutUint16(parity[14:16], 13)
			_, err = sender.Write(parity)
			require.NoError(t, err)
			if pending {
				_, err = sender.Write(packets[6])
				require.NoError(t, err)
			}

			binding, err := stun.Build(stun.TransactionID, stun.BindingRequest, stun.NewUsername("probe:sender"))
			require.NoError(t, err)
			_, err = sender.Write(binding.Raw)
			require.NoError(t, err)
			require.NoError(t, probe.SetReadDeadline(time.Now().Add(time.Second)))
			buffer := make([]byte, 1500)
			read, _, err := probe.ReadFrom(buffer)
			require.NoError(t, err)
			assert.Equal(t, binding.Raw, buffer[:read])
		})
	}
}

func TestRecoveryExpiresRequestsOutsideSenderHistory(t *testing.T) {
	for _, start := range []uint16{100, 65000} {
		conn, sender := recoverySocketPair(t)
		remote := conn.identity(sender.LocalAddr())
		stream := &recoveryStream{missing: make(map[uint16]time.Time), enabled: true}
		for offset := range recoveryHistorySize + 100 {
			if offset == 1 {
				continue
			}
			conn.trackGapLocked(recoveryPacketID{
				remote: remote, ssrc: 1, sequenceNumber: start + uint16(offset),
			}, stream)
			dueRepairs(stream, time.Now())
			if offset == recoveryHistorySize {
				assert.Contains(t, stream.missing, start+1)
			}
		}
		assert.Empty(t, stream.missing)
	}
}

func TestRecoveryDoesNotSharePacketsBetweenPublishers(t *testing.T) {
	receiver, sender := authenticatedRecoverySocketPair(t)
	serverAddress, ok := receiver.LocalAddr().(*net.UDPAddr)
	require.True(t, ok)
	other, err := net.DialUDP("udp4", nil, serverAddress)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, other.Close()) })

	packets := make([][]byte, 8)
	for index := range packets {
		packets[index] = testRecoveryRTP(500+uint16(index), byte(index+1))
		if index != 2 && index != 5 {
			_, err = sender.Write(packets[index])
			require.NoError(t, err)
		}
	}
	_, err = other.Write(packets[2])
	require.NoError(t, err)
	for range 7 {
		readRecoveryPacket(t, receiver)
	}

	_, err = sender.Write(testRecoveryParity(packets))
	require.NoError(t, err)
	require.NoError(t, receiver.SetReadDeadline(time.Now().Add(50*time.Millisecond)))
	_, _, err = receiver.ReadFrom(make([]byte, 1500))
	assert.Error(t, err)

	request := make([]byte, 1500)
	require.NoError(t, sender.SetReadDeadline(time.Now().Add(time.Second)))
	read, err := sender.Read(request)
	require.NoError(t, err)
	assert.Equal(t, byte(2), request[5])
	requested := []uint16{
		binary.BigEndian.Uint16(request[12:14]),
		binary.BigEndian.Uint16(request[14:read]),
	}
	assert.ElementsMatch(t, []uint16{502, 505}, requested)
}

func TestRecoveryPassesStandardWebRTCPacketsUnchanged(t *testing.T) {
	receiver, sender := recoverySocketPair(t)
	dtls := []byte{22, 0xfe, 0xfd, 0, 1}
	_, err := sender.Write(dtls)
	require.NoError(t, err)

	assert.Equal(t, dtls, readRecoveryPacket(t, receiver))
}

func recoverySocketPair(t *testing.T) (*recoveryConn, *net.UDPConn) {
	t.Helper()

	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	t.Cleanup(func() {
		if closeErr := server.Close(); !errors.Is(closeErr, net.ErrClosed) {
			require.NoError(t, closeErr)
		}
	})

	serverAddress, ok := server.LocalAddr().(*net.UDPAddr)
	require.True(t, ok)
	sender, err := net.DialUDP("udp4", nil, serverAddress)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sender.Close()) })

	log := logging.NewDefaultLoggerFactory().NewLogger("recovery-test")

	return &recoveryConn{UDPConn: server, log: log, recoveryState: newRecoveryState()}, sender
}

func authenticatedRecoverySocketPair(t *testing.T) (*recoveryConn, *net.UDPConn) {
	t.Helper()
	conn, sender := recoverySocketPair(t)
	bindRecoveryPath(t, conn, sender.LocalAddr(), "receiver:phone", true)
	// Drain the authenticated binding response before checking repair requests.
	require.NoError(t, sender.SetReadDeadline(time.Now().Add(time.Second)))
	_, err := sender.Read(make([]byte, 1500))
	require.NoError(t, err)

	return conn, sender
}

func TestUnauthenticatedRecoveryDoesNotAllocate(t *testing.T) {
	conn, sender := recoverySocketPair(t)
	packets := [][]byte{testRecoveryRTP(1, 1), testRecoveryRTP(10000, 2)}
	for _, packet := range packets {
		_, err := sender.Write(packet)
		require.NoError(t, err)
		assert.Equal(t, packet, readRecoveryPacket(t, conn))
	}
	_, err := sender.Write(testRecoveryParity(packets))
	require.NoError(t, err)
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(20*time.Millisecond)))
	_, _, err = conn.ReadFrom(make([]byte, 1500))
	require.Error(t, err)
	assert.Empty(t, conn.packets)
	assert.Empty(t, conn.streams)
	assert.Empty(t, conn.groups)
}

func TestRecoveryBudgetBypassesHistoryWithoutDroppingMedia(t *testing.T) {
	conn, sender := authenticatedRecoverySocketPair(t)
	conn.work = rate.NewLimiter(0, 0)
	packet := testRecoveryRTP(100, 1)
	_, err := sender.Write(packet)
	require.NoError(t, err)
	assert.Equal(t, packet, readRecoveryPacket(t, conn))
	assert.Empty(t, conn.packets)
	assert.Empty(t, conn.streams)
	_, recovered := conn.consumeParity(testRecoveryParity([][]byte{packet, testRecoveryRTP(101, 2)}), sender.LocalAddr())
	assert.False(t, recovered)
	assert.Empty(t, conn.groups)
}

func readRecoveryPacket(t *testing.T, receiver *recoveryConn) []byte {
	t.Helper()

	require.NoError(t, receiver.SetReadDeadline(time.Now().Add(time.Second)))
	buffer := make([]byte, 1500)
	read, _, err := receiver.ReadFrom(buffer)
	require.NoError(t, err)

	return append([]byte(nil), buffer[:read]...)
}

func testRecoveryRTP(sequenceNumber uint16, value byte) []byte {
	packet := []byte{0x80, 111, 0, 0, 0, 0, 0, 1, 1, 2, 3, 4, value, value + 1}
	binary.BigEndian.PutUint16(packet[2:4], sequenceNumber)

	return packet
}

func testRecoveryParity(packets [][]byte) []byte {
	maximumLength := 0
	for _, packet := range packets {
		maximumLength = max(maximumLength, len(packet))
	}

	parity := make([]byte, recoveryHeaderSize+len(packets)*4+maximumLength)
	copy(parity, "WGR1")
	parity[4] = recoveryParityType
	parity[5] = 8
	copy(parity[8:12], packets[0][8:12])
	for packetIndex, packet := range packets {
		offset := recoveryHeaderSize + packetIndex*4
		copy(parity[offset:offset+2], packet[2:4])
		binary.BigEndian.PutUint16(parity[offset+2:offset+4], 14)
		for byteIndex, value := range packet {
			parity[recoveryHeaderSize+len(packets)*4+byteIndex] ^= value
		}
	}

	return parity
}
