// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package ingest

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/Anywaystv/wagaStrim/internal/relay"
	"github.com/Anywaystv/wagaStrim/internal/stats"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"
)

const (
	previousResource    = "old"
	sessionCamera       = "camera"
	replacementResource = "new"
)

func TestOldSessionCannotForgetReplacement(t *testing.T) {
	server := &Server{
		sessions: map[string]*Session{
			previousResource:    {Resource: previousResource, IngestID: sessionCamera},
			replacementResource: {Resource: replacementResource, IngestID: sessionCamera},
		},
		byIngest: map[string]string{sessionCamera: replacementResource},
	}
	server.forget(previousResource)
	require.Equal(t, replacementResource, server.byIngest[sessionCamera])
	require.Len(t, server.sessions, 1)
}

func TestLateStopDoesNotDropReplacementMedia(t *testing.T) {
	old := &Session{Resource: previousResource, IngestID: sessionCamera}
	current := &Session{Resource: replacementResource, IngestID: sessionCamera}
	var stopped atomic.Int32
	hub := relay.New()
	server := &Server{
		sessions: map[string]*Session{previousResource: old, replacementResource: current},
		byIngest: map[string]string{sessionCamera: replacementResource},
		relay:    hub, stats: stats.New(hub),
		stopped: func(string) { stopped.Add(1) },
	}
	server.stats.Publishing(sessionCamera)
	track, err := server.relay.Publish(sessionCamera, webrtc.RTPCodecTypeVideo,
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000}, nil)
	require.NoError(t, err)
	server.stopSession(old)
	server.stopSession(old)
	require.Zero(t, stopped.Load())
	require.True(t, server.stats.Of(sessionCamera).Live)
	tracks, err := server.relay.Subscribe(sessionCamera)
	require.NoError(t, err)
	require.Equal(t, []*webrtc.TrackLocalStaticRTP{track}, tracks)
	require.Equal(t, replacementResource, server.byIngest[sessionCamera])
	server.stopSession(current)
	server.stopSession(current)
	require.EqualValues(t, 1, stopped.Load())
	require.Empty(t, server.sessions)
}

func TestConcurrentPublishKeepsOneSession(t *testing.T) {
	server, ing := newTestServer(t)
	first, _ := publisher(t)
	second, _ := publisher(t)
	offers := []string{offerFrom(t, first), offerFrom(t, second)}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, offer := range offers {
		go func() {
			<-start
			_, _, err := server.Publish(ing.SenderKey, offer)
			results <- err
		}()
	}
	close(start)
	accepted := 0
	for range 2 {
		select {
		case err := <-results:
			if err == nil {
				accepted++
			} else {
				require.ErrorIs(t, err, ErrAlreadyLive)
			}
		case <-time.After(5 * time.Second):
			require.FailNow(t, "concurrent publish did not finish")
		}
	}
	require.Positive(t, accepted)
	server.mu.Lock()
	active := len(server.sessions)
	owner := server.sessions[server.byIngest[ing.ID]]
	pending := len(server.pending)
	server.mu.Unlock()
	require.Equal(t, 1, active)
	require.NotNil(t, owner)
	require.Zero(t, pending)
}

func TestReplacementWaitsForCleanup(t *testing.T) {
	server, ing := newTestServer(t)
	first, _ := publisher(t)
	_, resource, err := server.Publish(ing.SenderKey, offerFrom(t, first))
	require.NoError(t, err)
	second, _ := publisher(t)
	offer := offerFrom(t, second)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	server.stopped = func(string) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
	}
	teardown := make(chan error, 1)
	go func() { teardown <- server.Teardown(resource) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		require.FailNow(t, "cleanup did not start")
	}
	result := make(chan error, 1)
	go func() { _, _, publishErr := server.Publish(ing.SenderKey, offer); result <- publishErr }()
	select {
	case err := <-result:
		close(release)
		require.FailNow(t, "replacement finished before cleanup", "error: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-result:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		require.FailNow(t, "replacement remained blocked")
	}
	require.NoError(t, <-teardown)
}

func TestFailedNegotiationReleasesCamera(t *testing.T) {
	server, ing := newTestServer(t)
	_, _, err := server.negotiate(*ing, webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: "invalid"})
	require.Error(t, err)
	server.mu.Lock()
	pending, active := len(server.pending), len(server.sessions)
	server.mu.Unlock()
	require.Zero(t, pending)
	require.Zero(t, active)
	peer, _ := publisher(t)
	_, _, err = server.Publish(ing.SenderKey, offerFrom(t, peer))
	require.NoError(t, err)
}
