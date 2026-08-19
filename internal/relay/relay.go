// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

// Package relay carries media from one publisher to any number of subscribers.
// Packets are forwarded, never decoded: what the phone encoded is what OBS gets.
package relay

import (
	"fmt"
	"sync"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// Stream is the live media of one ingest.
type Stream struct {
	mu sync.RWMutex

	tracks map[webrtc.RTPCodecType]*webrtc.TrackLocalStaticRTP

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
	stream.keyframe = keyframe
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

// Live reports whether an ingest currently has a publisher.
func (r *Relay) Live(ingestID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	_, ok := r.streams[ingestID]

	return ok
}

// Drop removes a stream when its publisher goes away. Subscribers stay attached
// to a silent track rather than being torn down, so a publisher reconnecting
// does not force every OBS source to be re-added.
func (r *Relay) Drop(ingestID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.streams, ingestID)
}

// Forward writes one packet to every subscriber of a track.
func Forward(track *webrtc.TrackLocalStaticRTP, pkt *rtp.Packet) error {
	if err := track.WriteRTP(pkt); err != nil {
		return fmt.Errorf("%w: %w", ErrBuildTrack, err)
	}

	return nil
}
