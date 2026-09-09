// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package ingest

import (
	"io"
	"os"
	"sync"
	"time"

	"github.com/pion/transport/v4/deadline"
	"github.com/pion/transport/v4/packetio"
)

const receiveBufferBytes = 1_000_000

// packetBuffer queues authenticated, decrypted RTP, not encrypted SRTP or
// playout frames. Storage grows on demand up to receiveBufferBytes. Reads
// block on notifications; the buffer owns no goroutine or periodic timer.
type packetBuffer struct {
	mu         sync.Mutex
	data       []byte
	head, used int
	closed     bool
	wake       chan struct{}
	done       chan struct{}
	deadline   *deadline.Deadline
	onPacket   func([]byte, time.Time)
}

func newPacketBuffer() *packetBuffer {
	return &packetBuffer{wake: make(chan struct{}, 1), done: make(chan struct{}), deadline: deadline.New()}
}

func (b *packetBuffer) Write(packet []byte) (int, error) {
	var arrival time.Time
	if b.onPacket != nil {
		arrival = time.Now()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, io.ErrClosedPipe
	}
	if len(packet) == 0 {
		return 0, nil
	}
	if len(packet) > 65535 {
		return 0, io.ErrShortBuffer
	}
	if b.onPacket != nil {
		b.onPacket(packet, arrival)
	}
	if b.used+len(packet)+2 > receiveBufferBytes {
		return 0, packetio.ErrFull
	}
	if b.used+len(packet)+2 > len(b.data) {
		data := make([]byte, min(receiveBufferBytes, max(2048, 2*len(b.data), b.used+len(packet)+2)))
		first := min(b.used, len(b.data)-b.head)
		copy(data, b.data[b.head:b.head+first])
		copy(data[first:], b.data[:b.used-first])
		b.data, b.head = data, 0
	}
	tail := (b.head + b.used) % len(b.data)
	b.data[tail] = byte((len(packet) >> 8) & 0xff)
	b.data[(tail+1)%len(b.data)] = byte(len(packet) & 0xff)
	tail = (tail + 2) % len(b.data)
	n := copy(b.data[tail:], packet)
	copy(b.data, packet[n:])
	b.used += len(packet) + 2
	select {
	case b.wake <- struct{}{}:
	default:
	}

	return len(packet), nil
}

func (b *packetBuffer) Read(packet []byte) (int, error) {
	for {
		b.mu.Lock()
		if b.deadline.Err() != nil {
			b.mu.Unlock()

			return 0, os.ErrDeadlineExceeded
		}
		if b.used > 0 {
			n, err := b.readPacket(packet)
			b.mu.Unlock()

			return n, err
		}
		if b.closed {
			b.mu.Unlock()

			return 0, io.EOF
		}
		expired := b.deadline.Done()
		b.mu.Unlock()
		select {
		case <-b.wake:
		case <-b.done:
		case <-expired:
		}
	}
}

// readPacket requires mu and a nonempty queue. Short reads discard the rest of
// that datagram, matching Pion's packetio contract.
func (b *packetBuffer) readPacket(packet []byte) (int, error) {
	size := int(b.data[b.head])<<8 | int(b.data[(b.head+1)%len(b.data)])
	start := (b.head + 2) % len(b.data)
	n := min(size, len(packet))
	first := copy(packet[:n], b.data[start:min(start+n, len(b.data))])
	copy(packet[first:n], b.data[:n-first])
	b.head = (start + size) % len(b.data)
	b.used -= size + 2
	if b.used == 0 {
		b.head = 0
	} else {
		select {
		case b.wake <- struct{}{}:
		default:
		}
	}
	if n < size {
		return n, io.ErrShortBuffer
	}

	return n, nil
}

func (b *packetBuffer) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed {
		b.closed = true
		b.deadline.Set(time.Time{})
		close(b.done)
	}

	return nil
}

func (b *packetBuffer) SetReadDeadline(t time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return io.ErrClosedPipe
	}
	b.deadline.Set(t)

	return nil
}

func receiveBuffer(kind packetio.BufferPacketType, _ uint32) io.ReadWriteCloser {
	if kind == packetio.RTPBufferPacket {
		return newPacketBuffer()
	}
	buffer := packetio.NewBuffer()
	buffer.SetLimitSize(100_000)

	return buffer
}
