// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

// Package ingest accepts WHIP publishers. One ingest carries at most one
// publisher at a time; the sender key decides which ingest a request lands on.
package ingest

import (
	"errors"
	"fmt"
	"io"
	"net/netip"
	"sync"
	"time"

	"github.com/MarcFryd/wagaStrim/internal/config"
	peerpkg "github.com/MarcFryd/wagaStrim/internal/peer"
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
	cfg    *config.Config
	log    logging.LeveledLogger
	api    *webrtc.API
	engine *webrtc.SettingEngine
	relay  *relay.Relay
	stats  *stats.Registry

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
	api, err := buildAPI(engine, config.AllCodecs())
	if err != nil {
		return nil, err
	}

	return &Server{
		cfg:      cfg,
		log:      log,
		relay:    hub,
		stats:    counters,
		api:      api,
		engine:   engine,
		sessions: map[string]*Session{},
		byIngest: map[string]string{},
	}, nil
}

// buildAPI assembles a WebRTC stack offering exactly the named video codecs.
// One is built per camera, because the toggles decide what the answer contains
// and the MediaEngine is what carries that decision.
func buildAPI(engine *webrtc.SettingEngine, codecs []string) (*webrtc.API, error) {
	media := &webrtc.MediaEngine{}
	if err := registerCodecs(media, codecs); err != nil {
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

	return webrtc.NewAPI(
		webrtc.WithMediaEngine(media),
		webrtc.WithInterceptorRegistry(registry),
		webrtc.WithSettingEngine(*engine),
	), nil
}

// registerCodecs registers everything the relay can carry. Which of them an
// ingest actually offers is decided per camera when the answer is built.
//
// Payload types match pion's own defaults, so a client that hardcodes them
// against a stock pion server still negotiates here.
func registerCodecs(media *webrtc.MediaEngine, codecs []string) error {
	wanted := map[string]bool{}
	for _, name := range codecs {
		wanted[name] = true
	}

	for _, entry := range videoCodecs() {
		if !wanted[entry.name] {
			continue
		}

		if err := media.RegisterCodec(entry.params, webrtc.RTPCodecTypeVideo); err != nil {
			return fmt.Errorf("%w: %s: %w", ErrBuildAPI, entry.name, err)
		}
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

type codecEntry struct {
	name   string
	params webrtc.RTPCodecParameters
}

func videoCodecs() []codecEntry {
	return []codecEntry{
		{config.CodecH264, webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType:     webrtc.MimeTypeH264,
				ClockRate:    90000,
				SDPFmtpLine:  "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
				RTCPFeedback: videoFeedback(),
			},
			PayloadType: 96,
		}},
		{config.CodecH265, webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType:     webrtc.MimeTypeH265,
				ClockRate:    90000,
				RTCPFeedback: videoFeedback(),
			},
			PayloadType: 116,
		}},
		{config.CodecAV1, webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType:     webrtc.MimeTypeAV1,
				ClockRate:    90000,
				RTCPFeedback: videoFeedback(),
			},
			PayloadType: 45,
		}},
	}
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
	sends, dirErr := peerpkg.Direction(desc, "sendonly")
	if dirErr != nil {
		return "", "", fmt.Errorf("%w: %w", ErrBadOffer, dirErr)
	}

	if !sends {
		return "", "", ErrNotSending
	}

	return s.negotiate(ing, desc)
}

func (s *Server) negotiate(ing config.Ingest, desc webrtc.SessionDescription) (string, string, error) {
	s.mu.Lock()
	_, live := s.byIngest[ing.ID]
	s.mu.Unlock()

	if live {
		return "", "", fmt.Errorf("%w: %s", ErrAlreadyLive, ing.Label)
	}

	// The camera's own codec set, not the server's. A toggle only means anything
	// if it changes what the answer offers.
	api, err := buildAPI(s.engine, ing.Codecs)
	if err != nil {
		return "", "", err
	}

	peer, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return "", "", fmt.Errorf("%w: %w", ErrBadOffer, err)
	}

	for _, kind := range []webrtc.RTPCodecType{webrtc.RTPCodecTypeVideo, webrtc.RTPCodecTypeAudio} {
		_, addErr := peer.AddTransceiverFromKind(kind,
			webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
		if addErr != nil {
			return "", "", peerpkg.Discard(peer, fmt.Errorf("%w: transceiver: %w", ErrBadOffer, addErr), s.log)
		}
	}

	session := &Session{IngestID: ing.ID, peer: peer}
	s.watch(ing, session, peer)

	answer, err := peerpkg.Answer(peer, desc)
	if err != nil {
		return "", "", peerpkg.Discard(peer, err, s.log)
	}

	resource, err := newResourceID()
	if err != nil {
		return "", "", peerpkg.Discard(peer, err, s.log)
	}

	session.Resource = resource

	s.mu.Lock()
	s.sessions[resource] = session
	s.byIngest[ing.ID] = resource
	s.mu.Unlock()

	return answer, resource, nil
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
		time.Duration(s.cfg.EffectiveDelay(ing.ID))*time.Millisecond,
		track.Codec().ClockRate,
		track.Codec().MimeType,
		askKeyframe,
	)
	defer buf.Close()

	s.relay.Track(ing.ID, buf)
	defer s.relay.Untrack(ing.ID, buf)

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

// watch wires the callbacks that track a publisher's life.
func (s *Server) watch(ing config.Ingest, session *Session, peer *webrtc.PeerConnection) {
	peer.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		s.drain(ing, session, peer, track)
	})

	s.watchPath(ing, peer)

	// Failed and Closed can both arrive, so the stop signal has to tolerate
	// being fired twice. pion happens to serialize these callbacks today, which
	// is not a property worth depending on.
	done := make(chan struct{})
	stopPairs := sync.OnceFunc(func() { close(done) })

	go s.watchPairs(ing.ID, peer, done)

	peer.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		s.log.Infof("ingest %s: %s", ing.Label, state)

		switch state {
		case webrtc.PeerConnectionStateConnected:
			s.stats.Publishing(ing.ID)
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			stopPairs()
			s.stats.Stopped(ing.ID)
			s.relay.Drop(ing.ID)
			s.forget(session.Resource)
		default:
		}
	})
}

// watchPath reports which candidate pair is carrying the stream. Renomination
// moves a phone from Wi-Fi to cellular without a reconnect, and from outside
// the process that is indistinguishable from nothing happening, so the move has
// to be surfaced or the feature is invisible.
func (s *Server) watchPath(ing config.Ingest, peer *webrtc.PeerConnection) {
	transport := peer.SCTP().Transport().ICETransport()
	if transport == nil {
		return
	}

	transport.OnSelectedCandidatePairChange(func(pair *webrtc.ICECandidatePair) {
		if pair == nil || pair.Local == nil || pair.Remote == nil {
			return
		}

		path := describePair(pair)
		s.log.Infof("ingest %s: now on %s", ing.Label, path)
		s.stats.Path(ing.ID, path)
	})
}

// describePair names a path in the terms a streamer thinks in. The candidate
// type is what says whether traffic is going direct or through a relay, which
// is the difference between working and working badly.
func describePair(pair *webrtc.ICECandidatePair) string {
	kind := "direct"

	switch pair.Remote.Typ {
	case webrtc.ICECandidateTypeRelay:
		kind = "relayed"
	case webrtc.ICECandidateTypeSrflx, webrtc.ICECandidateTypePrflx:
		kind = "through NAT"
	case webrtc.ICECandidateTypeHost:
		if isPrivate(pair.Remote.Address) {
			kind = "local network"
		}
	case webrtc.ICECandidateTypeUnknown:
	}

	return fmt.Sprintf("%s over %s via %s", kind, pair.Remote.Protocol, pair.Local.Address)
}

// isPrivate reports whether an address is on a local network rather than the
// internet, so a phone on the same Wi-Fi is not described as a direct hit from
// outside.
func isPrivate(address string) bool {
	addr, err := netip.ParseAddr(address)
	if err != nil {
		return false
	}

	return addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast()
}

// CloseIngest ends whatever is publishing to one camera. Deleting a camera has
// to disconnect it, or a revoked key keeps working until the phone gives up.
func (s *Server) CloseIngest(ingestID string) {
	s.mu.Lock()
	resource, ok := s.byIngest[ingestID]
	s.mu.Unlock()

	if !ok {
		return
	}

	if err := s.Teardown(resource); err != nil {
		s.log.Warnf("close ingest %s: %v", ingestID, err)
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

// session looks up a live session by resource id.
func (s *Server) session(resource string) (*Session, bool) {
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
