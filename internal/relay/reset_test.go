// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package relay

import (
	"testing"
	"time"

	"github.com/Anywaystv/wagaStrim/internal/dynamicdelay"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func resetPublisher(t *testing.T, mime string, key []byte, enabled bool) (*Relay, *Buffer, *Buffer) {
	t.Helper()
	hub, _ := livePublisher(t)
	hub.ConfigureDelay("cam", 2*time.Second, dynamicdelay.Options{Enabled: enabled})
	video, audio := clockBuffers(t, hub, time.Now().Add(-time.Hour-time.Second), mime)
	ntp := uint64(4_000_000_000) << 32
	for _, buf := range []*Buffer{video, audio} {
		buf.Push(packet(65530, 3600*buf.clockRate, key...))
		buf.SenderReport(3600*buf.clockRate, ntp)
	}
	assert.False(t, video.Correct())
	for _, buf := range []*Buffer{video, audio} {
		buf.SenderReport(0, ntp+(1<<32))
	}
	assert.False(t, video.Correct())

	return hub, video, audio
}

func TestSenderResetRejectsLatePackets(t *testing.T) {
	for _, codec := range []struct {
		mime string
		key  []byte
	}{
		{webrtc.MimeTypeH264, idr()},
		{webrtc.MimeTypeH265, []byte{19 << 1, 1}},
		{webrtc.MimeTypeAV1, []byte{8}},
	} {
		for _, enabled := range []bool{false, true} {
			t.Run(codec.mime+map[bool]string{false: "/fixed", true: "/dynamic"}[enabled], func(t *testing.T) {
				_, video, audio := resetPublisher(t, codec.mime, codec.key, enabled)
				before := video.playoutOf(0)

				for _, buf := range []*Buffer{video, audio} {
					// An old keyframe must not satisfy the new epoch's recovery.
					buf.Push(packet(65531, 3600*buf.clockRate+buf.clockRate/50, codec.key...))
					assert.Empty(t, buf.queue)
					assert.True(t, buf.catchUp)
				}
				assert.False(t, video.Correct())
				for _, buf := range []*Buffer{video, audio} {
					buf.Push(packet(65534, 0, codec.key...))
					require.Len(t, buf.queue, 1)
					// The old epoch can arrive again after the new keyframe.
					buf.Push(packet(65532, 3600*buf.clockRate+buf.clockRate/25, codec.key...))
					assert.Len(t, buf.queue, 1)
				}
				assert.False(t, video.Correct())
				for _, buf := range []*Buffer{video, audio} {
					buf.Push(packet(0, buf.clockRate/25, codec.key...))
					buf.Push(packet(65535, buf.clockRate/50, codec.key...))
					assert.Len(t, buf.queue, 3, "new media may reorder across sequence wrap")
					buf.SenderReport(buf.clockRate, uint64(4_000_000_002)<<32)
				}
				assert.False(t, video.Correct())
				assert.Equal(t, before.Add(20*time.Millisecond), video.playoutOf(video.clockRate/50))
				assert.Equal(t, video.playoutOf(video.clockRate/50), audio.playoutOf(audio.clockRate/50))
				assert.InDelta(t, 2000, float64(time.Until(before))/float64(time.Millisecond), 100)
			})
		}
	}
}

func TestLateResetKeyframeCannotTriggerDynamicJump(t *testing.T) {
	for _, resumed := range []bool{false, true} {
		t.Run(map[bool]string{false: "waiting", true: "resumed"}[resumed], func(t *testing.T) {
			hub, video, audio := resetPublisher(t, webrtc.MimeTypeH264, idr(), true)
			if resumed {
				for _, buf := range []*Buffer{video, audio} {
					buf.Push(packet(65534, 0, idr()...))
				}
			}
			control := &hub.streams["cam"].delay
			before := control.Step(time.Now().Add(time.Second), 10*time.Second)
			require.True(t, before.Pending)
			video.Push(packet(65531, 3600*video.clockRate, idr()...))
			assert.Same(t, before, control.State(), "old media must not change either track's clock")
		})
	}
}

func TestResetSequenceTrackingSurvivesWrapsAndLoss(t *testing.T) {
	_, video, audio := resetPublisher(t, webrtc.MimeTypeH264, idr(), false)
	for _, buf := range []*Buffer{video, audio} {
		for index, advance := range []uint32{0, 16000, 33000, 49000, 65000, 81000, 97000, 113000, 129000, 145000} {
			sequence := uint16((65534 + advance) & 0xffff)
			buf.Push(packet(sequence, advance, idr()...))
			require.Len(t, buf.queue, index+1)
		}
	}
}

func TestResetKeepsReorderedKeyframeParameters(t *testing.T) {
	_, video, _ := resetPublisher(t, webrtc.MimeTypeH264, idr(), false)
	video.Push(packet(65534, 0, idr()...))
	video.Push(packet(65532, 0, h264SPS))
	video.Push(packet(65533, 0, 8))
	assert.Len(t, video.queue, 3, "parameter sets preceding the IDR still belong to its new timestamp epoch")
}
