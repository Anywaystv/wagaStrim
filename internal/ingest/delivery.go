// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package ingest

import (
	"crypto/sha256"
	"net"
	"time"
)

type deliveryBatch struct {
	identity string
	lastSent time.Time
	tokens   [8][16]byte
	count    byte
}

// Receipts prove ciphertext arrival, not successful SRTP authentication or decode.
// Only recovery-enabled ICE peers receive them. Flushing on media arrival keeps
// the read path single-threaded; an incomplete final batch may expire at the sender.
func (c *recoveryConn) deliveryReceipt(id recoveryPacketID, data []byte, remote net.Addr, now time.Time) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	stream := c.streams[recoveryStreamID{remote: id.remote, ssrc: id.ssrc}]
	if stream == nil || !stream.enabled || len(data) > 8192 {
		return nil
	}
	if c.deliveries == nil {
		c.deliveries = make(map[string]*deliveryBatch)
	}
	key := remote.String()
	batch := c.deliveries[key]
	if batch == nil || batch.identity != id.remote {
		if len(c.deliveries) >= recoveryStreamLimit {
			clear(c.deliveries)
		}
		batch = &deliveryBatch{identity: id.remote}
		c.deliveries[key] = batch
	}
	digest := sha256.Sum256(data)
	copy(batch.tokens[batch.count][:], digest[:16])
	batch.count++
	if batch.count < 8 && now.Sub(batch.lastSent) < 20*time.Millisecond {
		return nil
	}
	batch.lastSent = now

	return batch.flush()
}

func (batch *deliveryBatch) flush() []byte {
	receipt := make([]byte, 8+int(batch.count)*16)
	copy(receipt, "WGR1")
	receipt[4] = 3
	receipt[5] = batch.count
	for index := range int(batch.count) {
		copy(receipt[8+index*16:], batch.tokens[index][:])
	}
	batch.count = 0

	return receipt
}
