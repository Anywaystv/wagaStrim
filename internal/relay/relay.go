// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

// Package relay carries media from one publisher to any number of subscribers.
// Packets are forwarded, never decoded: what the phone encoded is what OBS gets.
package relay

import (
	"fmt"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

// correctionInterval is how often a buffer is checked for drift. Faster than
// this is noise; slower and a growing buffer takes too long to notice.
const correctionInterval = time.Second

// Stream is the live media of one ingest.
type Stream struct {
	mu sync.RWMutex

	tracks  map[webrtc.RTPCodecType]*webrtc.TrackLocalStaticRTP
	buffers []*Buffer

	// keyframe asks the publisher for an IDR. A subscriber joining mid-stream
	// otherwise shows nothing until the encoder happens to emit one, which on a
	// long GOP is seconds of black.
	keyframe func()
}

// Relay holds one stream per publishing ingest.
type Relay struct {
	mu      sync.RWMutex
	streams map[string]*Stream
}

// New builds an empty relay.
func New() *Relay {
	return &Relay{streams: map[string]*Stream{}}
}

// Publish registers an outbound track for one inbound track and returns the
// writer the ingest forwards packets into.
func (r *Relay) Publish(
	ingestID string,
	kind webrtc.RTPCodecType,
	codec webrtc.RTPCodecCapability,
	keyframe func(),
) (*webrtc.TrackLocalStaticRTP, error) {
	track, err := webrtc.NewTrackLocalStaticRTP(codec, kind.String(), ingestID)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrBuildTrack, kind, err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	stream, ok := r.streams[ingestID]
	if !ok {
		stream = &Stream{tracks: map[webrtc.RTPCodecType]*webrtc.TrackLocalStaticRTP{}}
		r.streams[ingestID] = stream
	}

	stream.mu.Lock()
	stream.tracks[kind] = track

	// Only the video track's callback is kept. Audio calls this too, and storing
	// its callback would point the keyframe request at the audio SSRC, where a
	// PLI means nothing and no picture ever arrives.
	if kind == webrtc.RTPCodecTypeVideo {
		stream.keyframe = keyframe
	}

	stream.mu.Unlock()

	return track, nil
}

// Subscribe returns the tracks a receiver should attach, and asks the publisher
// for a keyframe so the picture appears immediately rather than at the next IDR.
func (r *Relay) Subscribe(ingestID string) ([]*webrtc.TrackLocalStaticRTP, error) {
	r.mu.RLock()
	stream, ok := r.streams[ingestID]
	r.mu.RUnlock()

	if !ok {
		return nil, ErrNotPublishing
	}

	stream.mu.RLock()
	tracks := make([]*webrtc.TrackLocalStaticRTP, 0, len(stream.tracks))

	for _, track := range stream.tracks {
		tracks = append(tracks, track)
	}

	keyframe := stream.keyframe
	stream.mu.RUnlock()

	if len(tracks) == 0 {
		return nil, ErrNotPublishing
	}

	if keyframe != nil {
		keyframe()
	}

	return tracks, nil
}

// Track registers a running buffer so a delay change can reach it. A camera in a
// sync group is retargeted when any member's delay moves, not only its own.
func (r *Relay) Track(ingestID string, buf *Buffer) {
	r.mu.RLock()
	stream, ok := r.streams[ingestID]
	r.mu.RUnlock()

	if !ok {
		return
	}

	stream.mu.Lock()
	defer stream.mu.Unlock()

	stream.buffers = append(stream.buffers, buf)
}

// Untrack forgets a buffer whose track has ended. Without it a link that
// reconnects repeatedly leaves a closed buffer behind on every attempt, and
// Retarget walks a list that only ever grows.
func (r *Relay) Untrack(ingestID string, buf *Buffer) {
	r.mu.RLock()
	stream, ok := r.streams[ingestID]
	r.mu.RUnlock()

	if !ok {
		return
	}

	stream.mu.Lock()
	defer stream.mu.Unlock()

	for idx, held := range stream.buffers {
		if held == buf {
			stream.buffers = append(stream.buffers[:idx], stream.buffers[idx+1:]...)

			return
		}
	}
}

// Retarget moves every running buffer of one ingest to a new playout target.
func (r *Relay) Retarget(ingestID string, target time.Duration) {
	r.mu.RLock()
	stream, ok := r.streams[ingestID]
	r.mu.RUnlock()

	if !ok {
		return
	}

	stream.mu.RLock()
	buffers := make([]*Buffer, len(stream.buffers))
	copy(buffers, stream.buffers)
	stream.mu.RUnlock()

	for _, buf := range buffers {
		buf.SetTarget(target)
	}
}

// Drop removes a stream when its publisher goes away. Its subscribers are not
// left behind: they hold the track object this stream was writing into, and the
// publisher gets a fresh one when it returns, so the ingest server disconnects
// them in the same breath as calling this.
func (r *Relay) Drop(ingestID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.streams, ingestID)
}

// Feed runs one track's buffer: packets pushed in are written out on their
// scheduled playout, and drift is corrected while it runs. It returns when the
// buffer is closed.
func Feed(track *webrtc.TrackLocalStaticRTP, buf *Buffer, onError func(error)) {
	stop := make(chan struct{})
	defer close(stop)

	go func() {
		tick := time.NewTicker(correctionInterval)
		defer tick.Stop()

		for {
			select {
			case <-tick.C:
				buf.Correct()
			case <-stop:
				return
			}
		}
	}()

	for {
		pkt, ok := buf.Pop()
		if !ok {
			return
		}

		if err := track.WriteRTP(pkt); err != nil {
			onError(fmt.Errorf("%w: %w", ErrBuildTrack, err))
		}
	}
}
