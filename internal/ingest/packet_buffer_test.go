// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package ingest

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"testing"
	"time"

	"github.com/pion/transport/v4/packetio"
	"github.com/stretchr/testify/require"
)

func TestPacketBufferWrapAndBounds(t *testing.T) {
	buffer := newPacketBuffer()
	t.Cleanup(func() { require.NoError(t, buffer.Close()) })
	const packetSize = 1300
	const count = receiveBufferBytes / (packetSize + 2)
	packet := bytes.Repeat([]byte{0xab}, packetSize)
	out := make([]byte, len(packet))
	for index := range count {
		binary.BigEndian.PutUint32(packet, uint32(index))
		_, err := buffer.Write(packet)
		require.NoError(t, err)
	}
	_, err := buffer.Write(packet)
	require.ErrorIs(t, err, packetio.ErrFull)
	// Keep the queue nonempty to exercise header and payload wraparound.
	for index := range 3000 {
		n, readErr := buffer.Read(out)
		require.NoError(t, readErr)
		require.Equal(t, uint32(index), binary.BigEndian.Uint32(out[:n]))
		require.Equal(t, packet[4:], out[4:n])
		binary.BigEndian.PutUint32(packet, uint32(index)+uint32(count))
		_, writeErr := buffer.Write(packet)
		require.NoError(t, writeErr)
	}
	require.Len(t, buffer.data, receiveBufferBytes)
	for range count {
		_, err = buffer.Read(out)
		require.NoError(t, err)
	}
	require.Zero(t, buffer.used)
}

func TestPacketBufferCloseWakesReaders(t *testing.T) {
	for range 50 {
		buffer := newPacketBuffer()
		done := make(chan error, 4)
		for range 4 {
			go func() { _, err := buffer.Read(make([]byte, 1500)); done <- err }()
		}
		require.NoError(t, buffer.Close())
		require.NoError(t, buffer.Close())
		for range 4 {
			select {
			case err := <-done:
				require.ErrorIs(t, err, io.EOF)
			case <-time.After(time.Second):
				require.FailNow(t, "reader survived buffer shutdown")
			}
		}
		_, err := buffer.Write([]byte{1})
		require.ErrorIs(t, err, io.ErrClosedPipe)
	}
}

func TestPacketBufferGrowsWithWrappedPackets(t *testing.T) {
	buffer := newPacketBuffer()
	t.Cleanup(func() { require.NoError(t, buffer.Close()) })
	first := bytes.Repeat([]byte{1}, 1000)
	second := bytes.Repeat([]byte{2}, 1000)
	third := bytes.Repeat([]byte{3}, 1000)
	large := bytes.Repeat([]byte{4}, 2000)
	for _, packet := range [][]byte{first, second} {
		_, err := buffer.Write(packet)
		require.NoError(t, err)
	}
	out := make([]byte, 2000)
	_, err := buffer.Read(out)
	require.NoError(t, err)
	_, err = buffer.Write(third)
	require.NoError(t, err)
	require.Len(t, buffer.data, 2048)
	_, err = buffer.Write(large)
	require.NoError(t, err)
	for _, packet := range [][]byte{second, third, large} {
		n, readErr := buffer.Read(out)
		require.NoError(t, readErr)
		require.Equal(t, packet, out[:n])
	}
}

func TestPacketBufferDeadlineAndDrain(t *testing.T) {
	buffer := newPacketBuffer()
	t.Cleanup(func() { require.NoError(t, buffer.Close()) })
	require.NoError(t, buffer.SetReadDeadline(time.Now().Add(10*time.Millisecond)))
	_, err := buffer.Read(make([]byte, 1500))
	require.ErrorIs(t, err, os.ErrDeadlineExceeded)
	require.NoError(t, buffer.SetReadDeadline(time.Time{}))
	_, err = buffer.Write([]byte{1, 2, 3})
	require.NoError(t, err)
	_, err = buffer.Read(make([]byte, 2))
	require.ErrorIs(t, err, io.ErrShortBuffer)
	_, err = buffer.Write([]byte{4})
	require.NoError(t, err)
	require.NoError(t, buffer.Close())
	out := make([]byte, 4)
	n, err := buffer.Read(out)
	require.NoError(t, err)
	require.Equal(t, []byte{4}, out[:n])
	_, err = buffer.Read(out)
	require.ErrorIs(t, err, io.EOF)
}

func BenchmarkReceiveBuffer(b *testing.B) {
	for _, kind := range []string{"waga", "pion"} {
		b.Run(kind, func(b *testing.B) {
			var buffer io.ReadWriteCloser
			if kind == "waga" {
				buffer = newPacketBuffer()
			} else {
				p := packetio.NewBuffer()
				p.SetLimitSize(receiveBufferBytes)
				buffer = p
			}
			defer func() { require.NoError(b, buffer.Close()) }()
			packet := make([]byte, 1200)
			out := make([]byte, 1500)
			_, _ = buffer.Write(packet)
			_, _ = buffer.Read(out)
			b.ReportAllocs()
			b.SetBytes(int64(len(packet)))
			b.ResetTimer()
			for range b.N {
				if _, err := buffer.Write(packet); err != nil {
					b.Fatal(err)
				}
				if _, err := buffer.Read(out); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
