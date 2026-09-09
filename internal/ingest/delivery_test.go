// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package ingest

import (
	"crypto/sha256"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeliveryReceiptsRequireRecoveryAndBatchByPath(t *testing.T) {
	conn, sender := recoverySocketPair(t)
	packet := testRecoveryRTP(1, 42)
	id, ok := recoveryRTPPacketID(packet)
	require.True(t, ok)
	id.remote = "ice:receiver:phone"
	now := time.Now()
	assert.Nil(t, conn.deliveryReceipt(id, packet, sender.LocalAddr(), now))
	conn.streams[recoveryStreamID{remote: id.remote, ssrc: id.ssrc}] = &recoveryStream{enabled: true}
	receipt := conn.deliveryReceipt(id, packet, sender.LocalAddr(), now)
	require.Len(t, receipt, 24)
	assert.Equal(t, []byte{'W', 'G', 'R', '1', 3, 1, 0, 0}, receipt[:8])
	digest := sha256.Sum256(packet)
	assert.Equal(t, digest[:16], receipt[8:])
	for range 7 {
		assert.Nil(t, conn.deliveryReceipt(id, packet, sender.LocalAddr(), now.Add(time.Millisecond)))
	}
	receipt = conn.deliveryReceipt(id, packet, sender.LocalAddr(), now.Add(time.Millisecond))
	require.Len(t, receipt, 136)
	assert.Equal(t, byte(8), receipt[5])
	assert.Nil(t, conn.deliveryReceipt(id, packet, sender.LocalAddr(), now.Add(2*time.Millisecond)))
	receipt = conn.deliveryReceipt(id, packet, sender.LocalAddr(), now.Add(22*time.Millisecond))
	require.Len(t, receipt, 40)
	assert.Equal(t, byte(2), receipt[5])
}

func TestDeliveryNewSessionCannotReusePendingBatch(t *testing.T) {
	conn, sender := recoverySocketPair(t)
	packet := testRecoveryRTP(1, 42)
	id, ok := recoveryRTPPacketID(packet)
	require.True(t, ok)
	id.remote = "ice:first"
	conn.streams[recoveryStreamID{remote: id.remote, ssrc: id.ssrc}] = &recoveryStream{enabled: true}
	now := time.Now()
	require.NotEmpty(t, conn.deliveryReceipt(id, packet, sender.LocalAddr(), now))
	require.Empty(t, conn.deliveryReceipt(id, packet, sender.LocalAddr(), now.Add(time.Millisecond)))
	id.remote = "ice:second"
	conn.streams[recoveryStreamID{remote: id.remote, ssrc: id.ssrc}] = &recoveryStream{enabled: true}
	receipt := conn.deliveryReceipt(id, packet, sender.LocalAddr(), now.Add(2*time.Millisecond))
	require.Len(t, receipt, 24)
	assert.Equal(t, byte(1), receipt[5])
}

func TestDeliveryReceiptTraversesAuthenticatedUDPSocket(t *testing.T) {
	conn, sender := recoverySocketPair(t)
	bindRecoveryPath(t, conn, sender.LocalAddr(), "receiver:phone", true)
	require.NoError(t, sender.SetReadDeadline(time.Now().Add(time.Second)))
	buffer := make([]byte, 1500)
	_, err := sender.Read(buffer)
	require.NoError(t, err)
	packet := testRecoveryRTP(123, 42)
	id, ok := recoveryRTPPacketID(packet)
	require.True(t, ok)
	id.remote = conn.identity(sender.LocalAddr())
	conn.streams[recoveryStreamID{remote: id.remote, ssrc: id.ssrc}] = &recoveryStream{
		enabled: true, missing: make(map[uint16]time.Time),
	}
	_, err = sender.Write(packet)
	require.NoError(t, err)
	assert.Equal(t, packet, readRecoveryPacket(t, conn))
	count, err := sender.Read(buffer)
	require.NoError(t, err)
	require.Equal(t, 24, count)
	digest := sha256.Sum256(packet)
	assert.Equal(t, digest[:16], buffer[8:count])
}
