// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

// Package egress serves subscribers over WHEP. A subscriber is usually the
// player page inside an OBS Browser Source, but anything speaking WHEP works.
package egress

import (
	"embed"
	"fmt"
	"sync"

	"github.com/MarcFryd/wagaStrim/internal/config"
	peerpkg "github.com/MarcFryd/wagaStrim/internal/peer"
	"github.com/MarcFryd/wagaStrim/internal/relay"
	"github.com/pion/logging"
	"github.com/pion/webrtc/v4"
)

//go:embed all:web
var assets embed.FS

// PlayerPage is the page an OBS Browser Source points at. It carries no
// configuration: the receiver key in the URL path is the whole setup.
func PlayerPage() ([]byte, error) {
	page, err := assets.ReadFile("web/player.html")
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBadOffer, err)
	}

	return page, nil
}

// Session is one attached subscriber.
type Session struct {
	Resource string
	IngestID string

	peer *webrtc.PeerConnection
}

// Server negotiates WHEP subscribers against the relay.
type Server struct {
	cfg   *config.Config
	log   logging.LeveledLogger
	api   *webrtc.API
	relay *relay.Relay

	mu       sync.Mutex
	sessions map[string]*Session
}

// NewServer shares the caller's WebRTC stack rather than building a second one.
func NewServer(cfg *config.Config, log logging.LeveledLogger, api *webrtc.API, hub *relay.Relay) *Server {
	return &Server{
		cfg:      cfg,
		log:      log,
		api:      api,
		relay:    hub,
		sessions: map[string]*Session{},
	}
}

// Subscribe negotiates a WHEP offer and returns the answer plus a resource id.
func (s *Server) Subscribe(key, offer string) (answer string, resource string, err error) {
	ing, role := s.cfg.Resolve(key)

	switch role {
	case config.RoleNone:
		return "", "", ErrUnknownKey
	case config.RoleSender:
		return "", "", ErrWrongRole
	case config.RoleReceiver:
	}

	desc := webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer}
	receives, dirErr := peerpkg.Direction(desc, "recvonly")
	if dirErr != nil {
		return "", "", fmt.Errorf("%w: %w", ErrBadOffer, dirErr)
	}

	if !receives {
		return "", "", ErrNotReceiving
	}

	tracks, err := s.relay.Subscribe(ing.ID)
	if err != nil {
		return "", "", fmt.Errorf("%w: %s", ErrOffline, ing.Label)
	}

	return s.negotiate(ing, desc, tracks)
}

func (s *Server) negotiate(
	ing config.Ingest,
	desc webrtc.SessionDescription,
	tracks []*webrtc.TrackLocalStaticRTP,
) (string, string, error) {
	peer, err := s.api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return "", "", fmt.Errorf("%w: %w", ErrBadOffer, err)
	}

	for _, track := range tracks {
		sender, addErr := peer.AddTrack(track)
		if addErr != nil {
			return "", "", peerpkg.Discard(peer, fmt.Errorf("%w: add track: %w", ErrBadOffer, addErr), s.log)
		}

		// RTCP from the subscriber has to be read or it backs up. Nothing acts on
		// it yet; phase 8 surfaces it as per-subscriber stats.
		go drainRTCP(sender)
	}

	answer, err := peerpkg.Answer(peer, desc)
	if err != nil {
		return "", "", peerpkg.Discard(peer, err, s.log)
	}

	resource, err := config.NewResourceKey()
	if err != nil {
		return "", "", peerpkg.Discard(peer, fmt.Errorf("%w: %w", ErrBadOffer, err), s.log)
	}

	session := &Session{Resource: resource, IngestID: ing.ID, peer: peer}

	peer.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		s.log.Infof("subscriber on %s: %s", ing.Label, state)

		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			s.forget(resource)
		}
	})

	s.mu.Lock()
	s.sessions[resource] = session
	s.mu.Unlock()

	return answer, resource, nil
}

func drainRTCP(sender *webrtc.RTPSender) {
	buf := make([]byte, 1500)

	for {
		if _, _, err := sender.Read(buf); err != nil {
			return
		}
	}
}

// CloseIngest disconnects every subscriber of one camera.
func (s *Server) CloseIngest(ingestID string) {
	s.mu.Lock()
	doomed := make([]*Session, 0, len(s.sessions))

	for resource, session := range s.sessions {
		if session.IngestID == ingestID {
			doomed = append(doomed, session)
			delete(s.sessions, resource)
		}
	}
	s.mu.Unlock()

	for _, session := range doomed {
		if err := session.peer.Close(); err != nil {
			s.log.Warnf("close subscriber: %v", err)
		}
	}
}

// Teardown ends a subscriber named by its WHEP resource id.
func (s *Server) Teardown(resource string) error {
	s.mu.Lock()
	session, ok := s.sessions[resource]
	delete(s.sessions, resource)
	s.mu.Unlock()

	if !ok {
		return ErrNoSession
	}

	if err := session.peer.Close(); err != nil {
		return fmt.Errorf("%w: %w", ErrNoSession, err)
	}

	return nil
}

func (s *Server) forget(resource string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.sessions, resource)
}

// Close ends every subscriber.
func (s *Server) Close() {
	s.mu.Lock()
	live := make([]*Session, 0, len(s.sessions))

	for _, session := range s.sessions {
		live = append(live, session)
	}

	s.sessions = map[string]*Session{}
	s.mu.Unlock()

	for _, session := range live {
		if err := session.peer.Close(); err != nil {
			s.log.Warnf("close subscriber: %v", err)
		}
	}
}
