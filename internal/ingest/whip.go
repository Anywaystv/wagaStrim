// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

// Package ingest accepts WHIP publishers. One ingest carries at most one
// publisher at a time; the sender key decides which ingest a request lands on.
package ingest

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/MarcFryd/wagaStrim/internal/config"
	"github.com/MarcFryd/wagaStrim/internal/relay"
	"github.com/MarcFryd/wagaStrim/internal/stats"
	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/nack"
	"github.com/pion/logging"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
)

// nackHistory must be at least as deep as the playout buffer or the buffer holds
// media it can no longer ask to have retransmitted. 4096 packets covers two
// seconds up to roughly 20 Mbps. Legal sizes are powers of two.
const nackHistory = 4096

// Session is one live publisher.
type Session struct {
	Resource string
	IngestID string

	peer  *webrtc.PeerConnection
	bytes uint64
	mu    sync.Mutex
}

// Bytes reports how much media has arrived on this session.
func (s *Session) Bytes() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.bytes
}

func (s *Session) add(count int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.bytes += uint64(count) //nolint:gosec // count comes from a read length and is never negative.
}

// Server holds the shared WebRTC stack and the live sessions.
type Server struct {
	cfg   *config.Config
	log   logging.LeveledLogger
	api   *webrtc.API
	relay *relay.Relay
	stats *stats.Registry

	mu       sync.Mutex
	sessions map[string]*Session
	byIngest map[string]string
}

// NewServer builds the WebRTC stack once and shares it across sessions.
func NewServer(
	cfg *config.Config,
	log logging.LeveledLogger,
	engine *webrtc.SettingEngine,
	hub *relay.Relay,
	counters *stats.Registry,
) (*Server, error) {
	media := &webrtc.MediaEngine{}
	if err := registerCodecs(media); err != nil {
		return nil, err
	}

	registry := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(media, registry); err != nil {
		return nil, fmt.Errorf("%w: default interceptors: %w", ErrBuildAPI, err)
	}

	generator, err := nack.NewGeneratorInterceptor(nack.GeneratorSize(nackHistory))
	if err != nil {
		return nil, fmt.Errorf("%w: nack generator: %w", ErrBuildAPI, err)
	}

	registry.Add(generator)

	// The same depth outbound: a subscriber has to be able to ask for anything
	// the buffer still holds, or the extra history on the inbound side is wasted.
	responder, err := nack.NewResponderInterceptor(nack.ResponderSize(nackHistory))
	if err != nil {
		return nil, fmt.Errorf("%w: nack responder: %w", ErrBuildAPI, err)
	}

	registry.Add(responder)

	return &Server{
		cfg:   cfg,
		log:   log,
		relay: hub,
		stats: counters,
		api: webrtc.NewAPI(
			webrtc.WithMediaEngine(media),
			webrtc.WithInterceptorRegistry(registry),
			webrtc.WithSettingEngine(*engine),
		),
		sessions: map[string]*Session{},
		byIngest: map[string]string{},
	}, nil
}

// registerCodecs registers what phase 2 accepts. H.265 and AV1 arrive in phase 6
// once the receiver side is known to handle them.
func registerCodecs(media *webrtc.MediaEngine) error {
	video := webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:     webrtc.MimeTypeH264,
			ClockRate:    90000,
			SDPFmtpLine:  "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
			RTCPFeedback: videoFeedback(),
		},
		PayloadType: 96,
	}

	if err := media.RegisterCodec(video, webrtc.RTPCodecTypeVideo); err != nil {
		return fmt.Errorf("%w: h264: %w", ErrBuildAPI, err)
	}

	audio := webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:  webrtc.MimeTypeOpus,
			ClockRate: 48000,
			Channels:  2,
		},
		PayloadType: 111,
	}

	if err := media.RegisterCodec(audio, webrtc.RTPCodecTypeAudio); err != nil {
		return fmt.Errorf("%w: opus: %w", ErrBuildAPI, err)
	}

	return nil
}

func videoFeedback() []webrtc.RTCPFeedback {
	return []webrtc.RTCPFeedback{
		{Type: "nack"},
		{Type: "nack", Parameter: "pli"},
		{Type: "ccm", Parameter: "fir"},
		{Type: webrtc.TypeRTCPFBTransportCC},
	}
}

// Publish negotiates a WHIP offer and returns the answer plus a resource id.
func (s *Server) Publish(key, offer string) (answer string, resource string, err error) {
	ing, role := s.cfg.Resolve(key)

	switch role {
	case config.RoleNone:
		return "", "", ErrUnknownKey
	case config.RoleReceiver:
		return "", "", ErrWrongRole
	case config.RoleSender:
	}

	desc := webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer}
	if dirErr := offerSendsMedia(desc); dirErr != nil {
		return "", "", dirErr
	}

	return s.negotiate(ing, desc)
}

// offerSendsMedia rejects an offer that only wants to receive. A WHEP client
// pointed at this endpoint produces exactly that, and catching it here turns a
// silent black source into a diagnosis.
func offerSendsMedia(desc webrtc.SessionDescription) error {
	parsed, err := desc.Unmarshal()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrBadOffer, err)
	}

	for _, media := range parsed.MediaDescriptions {
		for _, attr := range media.Attributes {
			if attr.Key == "sendonly" || attr.Key == "sendrecv" {
				return nil
			}
		}
	}

	return ErrNotSending
}

func (s *Server) negotiate(ing config.Ingest, desc webrtc.SessionDescription) (string, string, error) {
	s.mu.Lock()
	_, live := s.byIngest[ing.ID]
	s.mu.Unlock()

	if live {
		return "", "", fmt.Errorf("%w: %s", ErrAlreadyLive, ing.Label)
	}

	peer, err := s.api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return "", "", fmt.Errorf("%w: %w", ErrBadOffer, err)
	}

	for _, kind := range []webrtc.RTPCodecType{webrtc.RTPCodecTypeVideo, webrtc.RTPCodecTypeAudio} {
		_, addErr := peer.AddTransceiverFromKind(kind,
			webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
		if addErr != nil {
			return "", "", s.abort(peer, fmt.Errorf("%w: transceiver: %w", ErrBadOffer, addErr))
		}
	}

	session := &Session{IngestID: ing.ID, peer: peer}
	peer.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) { s.drain(ing, session, peer, track) })
	peer.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		s.log.Infof("ingest %s: %s", ing.Label, state)

		if state == webrtc.PeerConnectionStateConnected {
			s.stats.Publishing(ing.ID)
		}

		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			s.stats.Stopped(ing.ID)
			s.relay.Drop(ing.ID)
			s.forget(session.Resource)
		}
	})

	answer, err := s.exchange(peer, desc)
	if err != nil {
		return "", "", s.abort(peer, err)
	}

	resource, err := newResourceID()
	if err != nil {
		return "", "", s.abort(peer, err)
	}

	session.Resource = resource

	s.mu.Lock()
	s.sessions[resource] = session
	s.byIngest[ing.ID] = resource
	s.mu.Unlock()

	return answer, resource, nil
}

// abort closes a half-built peer and returns the reason it was abandoned, so no
// negotiation failure leaves a PeerConnection and its ICE agent running.
func (s *Server) abort(peer *webrtc.PeerConnection, cause error) error {
	if err := peer.Close(); err != nil {
		s.log.Warnf("close aborted peer: %v", err)
	}

	return cause
}

// exchange applies the offer and returns a fully gathered answer. WHIP has no
// trickle path in this build, so gathering completes before the answer is sent.
func (s *Server) exchange(peer *webrtc.PeerConnection, desc webrtc.SessionDescription) (string, error) {
	if err := peer.SetRemoteDescription(desc); err != nil {
		return "", fmt.Errorf("%w: remote description: %w", ErrBadOffer, err)
	}

	answer, err := peer.CreateAnswer(nil)
	if err != nil {
		return "", fmt.Errorf("%w: create answer: %w", ErrBadOffer, err)
	}

	gathered := webrtc.GatheringCompletePromise(peer)

	if err := peer.SetLocalDescription(answer); err != nil {
		return "", fmt.Errorf("%w: local description: %w", ErrBadOffer, err)
	}

	<-gathered

	return peer.LocalDescription().SDP, nil
}

// drain forwards the track into the relay and counts bytes. Packets are passed
// through untouched: no depacketising, no re-encoding, no timestamp rewriting.
func (s *Server) drain(ing config.Ingest, session *Session, peer *webrtc.PeerConnection, track *webrtc.TrackRemote) {
	s.log.Infof("ingest %s: track %s %s", ing.Label, track.Kind(), track.Codec().MimeType)

	askKeyframe := func() { s.requestKeyframe(peer, track.SSRC()) }

	out, err := s.relay.Publish(ing.ID, track.Kind(), track.Codec().RTPCodecCapability, askKeyframe)
	if err != nil {
		s.log.Errorf("ingest %s: %v", ing.Label, err)

		return
	}

	buf := relay.NewBuffer(
		time.Duration(ing.DelayMS)*time.Millisecond,
		track.Codec().ClockRate,
		track.Codec().MimeType,
		askKeyframe,
	)
	defer buf.Close()

	go relay.Feed(out, buf, func(err error) { s.log.Warnf("ingest %s: forward: %v", ing.Label, err) })

	for {
		pkt, _, err := track.ReadRTP()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				s.log.Infof("ingest %s: track ended: %v", ing.Label, err)
			}

			return
		}

		session.add(pkt.MarshalSize())
		buf.Push(pkt)

		if track.Kind() == webrtc.RTPCodecTypeVideo {
			late, dropped := buf.Stats()
			s.stats.Observe(ing.ID, session.Bytes(), late, dropped)
		}
	}
}

// requestKeyframe asks the publisher for an IDR so a subscriber that just joined
// sees a picture now instead of at the next natural keyframe.
func (s *Server) requestKeyframe(peer *webrtc.PeerConnection, ssrc webrtc.SSRC) {
	err := peer.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: uint32(ssrc)}})
	if err != nil {
		s.log.Warnf("keyframe request: %v", err)
	}
}

// Teardown ends a session named by its WHIP resource id.
func (s *Server) Teardown(resource string) error {
	s.mu.Lock()
	session, ok := s.sessions[resource]
	s.mu.Unlock()

	if !ok {
		return ErrNoSession
	}

	s.forget(resource)

	if err := session.peer.Close(); err != nil {
		return fmt.Errorf("%w: %w", ErrNoSession, err)
	}

	return nil
}

// Session looks up a live session by resource id.
func (s *Server) Session(resource string) (*Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[resource]

	return session, ok
}

func (s *Server) forget(resource string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if session, ok := s.sessions[resource]; ok {
		delete(s.byIngest, session.IngestID)
		delete(s.sessions, resource)
	}
}

// Close ends every live session.
func (s *Server) Close() {
	s.mu.Lock()
	live := make([]*Session, 0, len(s.sessions))

	for _, session := range s.sessions {
		live = append(live, session)
	}

	s.sessions = map[string]*Session{}
	s.byIngest = map[string]string{}
	s.mu.Unlock()

	for _, session := range live {
		if err := session.peer.Close(); err != nil {
			s.log.Warnf("close session: %v", err)
		}
	}
}

func newResourceID() (string, error) {
	key, err := config.NewResourceKey()
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrBuildAPI, err)
	}

	return key, nil
}

// API exposes the shared WebRTC stack so egress does not build a second one.
func (s *Server) API() *webrtc.API {
	return s.api
}
