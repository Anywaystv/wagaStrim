// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package ingest

import (
	"encoding/binary"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/logging"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/sdp/v3"
	"github.com/pion/transport/v4/packetio"
	"github.com/stretchr/testify/require"
)

func testArrivalFeedback(t *testing.T) *arrivalFeedback {
	t.Helper()
	feedback := newArrivalFeedback(logging.NewDefaultLoggerFactory().NewLogger("test"))
	t.Cleanup(func() { require.NoError(t, feedback.Close()) })
	feedback.BindRemoteStream(&interceptor.StreamInfo{SSRC: 42, RTPHeaderExtensions: []interceptor.RTPHeaderExtension{
		{URI: sdp.TransportCCURI, ID: 3},
	}}, nil)

	return feedback
}

func arrivalPacket(t *testing.T, sequence uint16) []byte {
	t.Helper()
	p := rtp.Packet{
		Header: rtp.Header{Version: 2, SSRC: 42, SequenceNumber: sequence, PayloadType: 96}, Payload: []byte{1},
	}
	ext := make([]byte, 2)
	binary.BigEndian.PutUint16(ext, sequence)
	require.NoError(t, p.SetExtension(3, ext))
	raw, err := p.Marshal()
	require.NoError(t, err)

	return raw
}

func TestArrivalFeedbackDoesNotWaitForBufferRead(t *testing.T) {
	feedback := testArrivalFeedback(t)
	buffer := feedback.buffer(packetio.RTPBufferPacket, 42)
	t.Cleanup(func() { require.NoError(t, buffer.Close()) })
	first, second := arrivalPacket(t, 10), arrivalPacket(t, 11)
	_, err := buffer.Write(first)
	require.NoError(t, err)
	_, err = buffer.Write(second)
	require.NoError(t, err)
	packets := feedback.feedback()
	require.Len(t, packets, 1, "feedback must exist while both packets are still unread")
	report, ok := packets[0].(*rtcp.TransportLayerCC)
	require.True(t, ok)
	require.EqualValues(t, 10, report.BaseSequenceNumber)
	require.EqualValues(t, 2, report.PacketStatusCount)
	out := make([]byte, 1500)
	for _, want := range [][]byte{first, second} {
		n, readErr := buffer.Read(out)
		require.NoError(t, readErr)
		require.Equal(t, want, out[:n])
	}
	require.Empty(t, feedback.feedback(), "reading must not report packets a second time")
}

func TestArrivalFeedbackUsesCaptureTimeAndDeduplicatesAcrossWrap(t *testing.T) {
	feedback := testArrivalFeedback(t)
	start := feedback.start.Add(time.Second)
	feedback.record(arrivalPacket(t, 65535), start)
	feedback.record(arrivalPacket(t, 0), start.Add(20*time.Millisecond))
	feedback.record(arrivalPacket(t, 0), start.Add(2*time.Second))
	packets := feedback.feedback()
	require.Len(t, packets, 1)
	report, ok := packets[0].(*rtcp.TransportLayerCC)
	require.True(t, ok)
	require.EqualValues(t, 65535, report.BaseSequenceNumber)
	require.EqualValues(t, 2, report.PacketStatusCount)
	require.Len(t, report.RecvDeltas, 2)
	require.EqualValues(t, 20_000, report.RecvDeltas[1].Delta)
}

func TestArrivalFeedbackIgnoresUnboundAndMalformedPackets(t *testing.T) {
	feedback := testArrivalFeedback(t)
	feedback.record([]byte{0x80}, time.Now())
	feedback.UnbindRemoteStream(&interceptor.StreamInfo{SSRC: 42})
	feedback.record(arrivalPacket(t, 10), time.Now())
	require.Empty(t, feedback.feedback())
}

func TestArrivalFeedbackSessionIsolationAndShutdown(t *testing.T) {
	feedback, other := testArrivalFeedback(t), testArrivalFeedback(t)
	_, err := feedback.NewInterceptor("")
	require.NoError(t, err)
	_, err = feedback.NewInterceptor("")
	require.Error(t, err)
	var writes atomic.Int32
	feedback.BindRTCPWriter(interceptor.RTCPWriterFunc(func([]rtcp.Packet, interceptor.Attributes) (int, error) {
		writes.Add(1)

		return 0, nil
	}))
	feedback.record(arrivalPacket(t, 10), time.Now())
	require.Empty(t, other.feedback())
	require.Eventually(t, func() bool { return writes.Load() > 0 }, time.Second, 10*time.Millisecond)
	require.NoError(t, feedback.Close())
	require.NoError(t, feedback.Close())
	feedback.record(arrivalPacket(t, 11), time.Now())
	require.Empty(t, feedback.feedback())
	buffer := feedback.buffer(packetio.RTPBufferPacket, 42)
	require.NoError(t, buffer.Close())
	_, err = buffer.Write(arrivalPacket(t, 12))
	require.ErrorIs(t, err, io.ErrClosedPipe)
}
