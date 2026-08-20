// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package relay

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// livePublisher returns a relay carrying one video stream on camera "cam", and
// the count of keyframe requests that reached its publisher.
func livePublisher(t *testing.T) (*Relay, *atomic.Int64) {
	t.Helper()

	asked := &atomic.Int64{}
	hub := New()

	_, err := hub.Publish("cam", webrtc.RTPCodecTypeVideo, videoCodec(), func() { asked.Add(1) })
	require.NoError(t, err)

	return hub, asked
}

func TestAKeyframeRequestReachesThePublisher(t *testing.T) {
	hub, asked := livePublisher(t)

	hub.Keyframe("cam")

	assert.Equal(t, int64(1), asked.Load())
}

func TestKeyframeRequestsAreThrottled(t *testing.T) {
	hub, asked := livePublisher(t)

	for range 10 {
		hub.Keyframe("cam")
	}

	require.Equal(t, int64(1), asked.Load(), "a burst of requests must reach the publisher once")

	stream := hub.streams["cam"]
	stream.mu.Lock()
	stream.lastKeyframe = time.Now().Add(-keyframeInterval - time.Millisecond)
	stream.mu.Unlock()

	hub.Keyframe("cam")

	assert.Equal(t, int64(2), asked.Load(), "a request after the interval must be passed on")
}

func TestAKeyframeRequestForAnIdleCameraIsDropped(t *testing.T) {
	hub, asked := livePublisher(t)

	hub.Keyframe("other")

	assert.Zero(t, asked.Load())
}

func TestSubscribingAsksForAKeyframe(t *testing.T) {
	hub, asked := livePublisher(t)

	tracks, err := hub.Subscribe("cam")
	require.NoError(t, err)
	require.Len(t, tracks, 1)

	assert.Equal(t, int64(1), asked.Load())
}

// A PLI naming the audio SSRC asks for a picture from a stream that has none,
// so the audio track's callback must never become the one this camera uses.
func TestAudioDoesNotTakeOverTheKeyframeRequest(t *testing.T) {
	hub, video := livePublisher(t)

	audio := &atomic.Int64{}
	codec := webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000}

	_, err := hub.Publish("cam", webrtc.RTPCodecTypeAudio, codec, func() { audio.Add(1) })
	require.NoError(t, err)

	hub.Keyframe("cam")

	assert.Equal(t, int64(1), video.Load(), "the video publisher must be the one asked")
	assert.Zero(t, audio.Load(), "an audio track must never be asked for a picture")
}
