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
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Anywaystv/wagaStrim/internal/config"
	peerpkg "github.com/Anywaystv/wagaStrim/internal/peer"
	"github.com/Anywaystv/wagaStrim/internal/relay"
	"github.com/Anywaystv/wagaStrim/internal/stats"
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

	peer     *webrtc.PeerConnection
	bytes    atomic.Uint64
	failover *pathFailover
	stopOnce sync.Once
	ended    bool // Protected by Server.mu.
}

// Bytes reports how much media has arrived on this session.
func (s *Session) Bytes() uint64 {
	return s.bytes.Load()
}

// Server holds the shared WebRTC stack and the live sessions.
type Server struct {
	cfg    *config.Config
	log    logging.LeveledLogger
	api    *webrtc.API
	engine *webrtc.SettingEngine
	relay  *relay.Relay
	stats  *stats.Registry

	// Disconnect subscribers when their publisher ends: a replacement gets
	// new track objects. The player reconnects rather than staying frozen.
	stopped func(ingestID string)

	mu       sync.Mutex
	sessions map[string]*Session
	byIngest map[string]string
	pending  map[string]bool
	closed   bool
}

// NewServer builds the shared egress API; publishers get their own API during negotiation.
func NewServer(
	cfg *config.Config,
	log logging.LeveledLogger,
	engine *webrtc.SettingEngine,
	hub *relay.Relay,
	counters *stats.Registry,
	stopped func(ingestID string),
) (*Server, error) {
	api, err := buildAPI(engine, config.AllCodecs(), nil)
	if err != nil {
		return nil, err
	}

	return &Server{
		cfg:      cfg,
		log:      log,
		relay:    hub,
		stats:    counters,
		stopped:  stopped,
		api:      api,
		engine:   engine,
		sessions: map[string]*Session{},
		byIngest: map[string]string{},
		pending:  map[string]bool{},
	}, nil
}

// buildAPI assembles a WebRTC stack with the camera's allowed video codecs.
func buildAPI(engine *webrtc.SettingEngine, codecs []string, arrival *arrivalFeedback) (*webrtc.API, error) {
	media := &webrtc.MediaEngine{}
	if err := registerCodecs(media, codecs); err != nil {
		return nil, err
	}

	// RegisterDefaultInterceptors would add a second NACK pair with default
	// history, duplicating retransmissions. Register each interceptor once.
	registry := &interceptor.Registry{}
	if err := webrtc.ConfigureRTCPReports(registry); err != nil {
		return nil, fmt.Errorf("%w: rtcp reports: %w", ErrBuildAPI, err)
	}

	if err := webrtc.ConfigureStatsInterceptor(registry); err != nil {
		return nil, fmt.Errorf("%w: stats: %w", ErrBuildAPI, err)
	}

	if err := configureArrivalFeedback(media, registry, arrival); err != nil {
		return nil, fmt.Errorf("%w: twcc: %w", ErrBuildAPI, err)
	}

	generator, err := nack.NewGeneratorInterceptor(nack.GeneratorSize(nackHistory))
	if err != nil {
		return nil, fmt.Errorf("%w: nack generator: %w", ErrBuildAPI, err)
	}

	registry.Add(generator)

	// Match outbound history so subscribers can recover buffered packets too.
	responder, err := nack.NewResponderInterceptor(nack.ResponderSize(nackHistory))
	if err != nil {
		return nil, fmt.Errorf("%w: nack responder: %w", ErrBuildAPI, err)
	}

	registry.Add(responder)

	// Request receiver buffering even when the subscriber is not our player page.
	if err := media.RegisterHeaderExtension(
		webrtc.RTPHeaderExtensionCapability{URI: playoutDelayURI},
		webrtc.RTPCodecTypeVideo,
		webrtc.RTPTransceiverDirectionSendonly,
	); err != nil {
		return nil, fmt.Errorf("%w: playout delay: %w", ErrBuildAPI, err)
	}

	registry.Add(playoutDelayFactory{})

	return webrtc.NewAPI(
		webrtc.WithMediaEngine(media),
		webrtc.WithInterceptorRegistry(registry),
		webrtc.WithSettingEngine(*engine),
	), nil
}

// registerCodecs enables the camera's video codecs and supported audio codecs.
// Negotiated payload types follow the offer. Opus's offered in-band FEC
// parameters are preserved in the answer; see TestPublisherOpusInBandFECIsPreservedInAnswer.
func registerCodecs(media *webrtc.MediaEngine, codecs []string) error {
	for _, entry := range videoCodecs() {
		if !slices.Contains(codecs, entry.name) {
			continue
		}

		if err := media.RegisterCodec(entry.params, webrtc.RTPCodecTypeVideo); err != nil {
			return fmt.Errorf("%w: %s: %w", ErrBuildAPI, entry.name, err)
		}
		// str0m uses negotiated RTX for padding probes as well as NACK repair.
		if err := media.RegisterCodec(webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType: "video/rtx", ClockRate: 90000,
				SDPFmtpLine: fmt.Sprintf("apt=%d", entry.params.PayloadType),
			},
			PayloadType: entry.params.PayloadType + 1,
		}, webrtc.RTPCodecTypeVideo); err != nil {
			return fmt.Errorf("%w: %s RTX: %w", ErrBuildAPI, entry.name, err)
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

	// The sender chooses the audio codec in its offer; accepting AAC does not
	// change an Opus publisher or add a decoder to the relay.
	aac := webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:  "audio/MPEG4-GENERIC",
			ClockRate: 48000,
			Channels:  2,
			SDPFmtpLine: "streamtype=5;profile-level-id=1;mode=AAC-hbr;config=1190;" +
				"sizelength=13;indexlength=3;indexdeltalength=3",
		},
		PayloadType: 112,
	}
	if err := media.RegisterCodec(aac, webrtc.RTPCodecTypeAudio); err != nil {
		return fmt.Errorf("%w: aac: %w", ErrBuildAPI, err)
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
	if s.closed || s.pending[ing.ID] {
		s.mu.Unlock()

		return "", "", fmt.Errorf("%w: %s", ErrAlreadyLive, ing.Label)
	}
	s.pending[ing.ID] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pending, ing.ID)
		s.mu.Unlock()
	}()

	if err := s.clearPrevious(ing); err != nil {
		return "", "", err
	}

	// Publisher-specific codecs, feedback, and failover state.
	failover := &pathFailover{}
	engine := *s.engine
	engine.SetICEBindingRequestHandler(failover.binding)
	arrival := newArrivalFeedback(s.log)
	engine.BufferFactory = arrival.buffer
	api, err := buildAPI(&engine, ing.Codecs, arrival)
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

	// Assign the resource before wiring callbacks so early failures can remove
	// the correct session.
	resource, err := config.NewResourceKey()
	if err != nil {
		return "", "", peerpkg.Discard(peer, fmt.Errorf("%w: %w", ErrMintResource, err), s.log)
	}

	session := &Session{Resource: resource, IngestID: ing.ID, peer: peer, failover: failover}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()

		return "", "", peerpkg.Discard(peer, ErrNoSession, s.log)
	}
	s.sessions[resource] = session
	s.byIngest[ing.ID] = resource
	s.watch(ing, session, peer)
	s.mu.Unlock()

	return s.answerSession(session, desc)
}

func (s *Server) answerSession(session *Session, desc webrtc.SessionDescription) (string, string, error) {
	answer, err := peerpkg.Answer(session.peer, desc)
	if err != nil {
		s.stopSession(session)

		return "", "", peerpkg.Discard(session.peer, err, s.log)
	}

	s.mu.Lock()
	current := s.currentLocked(session)
	s.mu.Unlock()
	if !current {
		return "", "", peerpkg.Discard(session.peer, ErrNoSession, s.log)
	}

	return answer, session.Resource, nil
}

// clearPrevious replaces disconnected or stopping publishers so retries need
// not wait for ICE failure. A connected publisher retains ownership.
func (s *Server) clearPrevious(ing config.Ingest) error {
	s.mu.Lock()
	resource, live := s.byIngest[ing.ID]
	session, known := s.sessions[resource]
	stopping := known && session.ended
	s.mu.Unlock()

	if !live || !known {
		return nil
	}

	if !stopping && session.peer.ConnectionState() == webrtc.PeerConnectionStateConnected {
		return fmt.Errorf("%w: %s", ErrAlreadyLive, ing.Label)
	}

	s.log.Infof("ingest %s: replacing a publisher that is %s", ing.Label, session.peer.ConnectionState())

	if err := s.Teardown(resource); err != nil && !errors.Is(err, ErrNoSession) {
		return fmt.Errorf("%w: %w", ErrAlreadyLive, err)
	}

	return nil
}

// drain forwards the track into the relay and counts bytes. Packets are passed
// through untouched: no depacketising, no re-encoding, no timestamp rewriting.
func (s *Server) drain(
	ing config.Ingest, session *Session, peer *webrtc.PeerConnection,
	track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver,
) {
	s.log.Infof("ingest %s: track %s %s", ing.Label, track.Kind(), track.Codec().MimeType)
	s.mu.Lock()
	if !s.currentLocked(session) {
		s.mu.Unlock()

		return
	}

	// Report the negotiated video codec, not the configured preference.
	if track.Kind() == webrtc.RTPCodecTypeVideo {
		s.stats.Codec(ing.ID, config.CodecLabelOf(track.Codec().MimeType))
	}

	// A PLI must name the video SSRC, never the audio SSRC.
	var askKeyframe func()
	if track.Kind() == webrtc.RTPCodecTypeVideo {
		askKeyframe = func() { s.requestKeyframe(ing, peer, track.SSRC()) }
	}

	out, err := s.relay.Publish(ing.ID, track.Kind(), track.Codec().RTPCodecCapability, askKeyframe)
	if err != nil {
		s.mu.Unlock()
		s.log.Errorf("ingest %s: %v", ing.Label, err)

		return
	}

	target := time.Duration(s.cfg.EffectiveDelay(ing.ID)) * time.Millisecond
	s.relay.ConfigureDelay(ing.ID, target, s.cfg.DelayOptions(ing.ID))
	buf := relay.NewBuffer(target, track.Codec().ClockRate, track.Codec().MimeType, askKeyframe)

	s.relay.Track(ing.ID, buf)
	s.mu.Unlock()
	defer func() {
		buf.Close()
		s.relay.Untrack(ing.ID, buf)
	}()
	go drainRTCP(receiver, track.SSRC(), buf)

	go relay.Feed(out, buf, func(err error) { s.log.Warnf("ingest %s: forward: %v", ing.Label, err) })

	for {
		pkt, _, err := track.ReadRTP()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				s.log.Infof("ingest %s: track ended: %v", ing.Label, err)
			}

			return
		}

		session.bytes.Add(uint64(pkt.MarshalSize())) //nolint:gosec // RTP packet sizes are nonnegative.
		buf.Push(pkt)

		s.mu.Lock()
		if s.currentLocked(session) {
			// Read the shared total here so audio and video cannot sample it out of order.
			total := session.Bytes()
			if track.Kind() == webrtc.RTPCodecTypeVideo {
				late, dropped := buf.Stats()
				s.stats.Observe(ing.ID, total, late, dropped)
			} else {
				s.stats.ObserveBytes(ing.ID, total)
			}
		}
		s.mu.Unlock()
	}
}

// requestKeyframe sends a PLI naming a publisher's video SSRC.
func (s *Server) requestKeyframe(ing config.Ingest, peer *webrtc.PeerConnection, ssrc webrtc.SSRC) {
	err := peer.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: uint32(ssrc)}})
	if err != nil {
		s.log.Warnf("ingest %s: keyframe request: %v", ing.Label, err)
	}
}

// drainRTCP drives the report interceptor so Sender Reports contribute to
// Receiver Reports, and retains their clock mapping for synchronized playback.
func drainRTCP(receiver *webrtc.RTPReceiver, ssrc webrtc.SSRC, buf *relay.Buffer) {
	for {
		packets, _, err := receiver.ReadRTCP()
		if err != nil {
			return
		}
		for _, packet := range packets {
			if report, ok := packet.(*rtcp.SenderReport); ok && report.SSRC == uint32(ssrc) {
				buf.SenderReport(report.RTPTime, report.NTPTime)
			}
		}
	}
}

// watch wires the callbacks that track a publisher's life.
func (s *Server) watch(ing config.Ingest, session *Session, peer *webrtc.PeerConnection) {
	peer.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		s.drain(ing, session, peer, track, receiver)
	})

	s.watchPath(ing, session, peer)

	// Failed and Closed can both arrive; stop the watcher only once.
	done := make(chan struct{})
	stopPairs := sync.OnceFunc(func() { close(done) })

	go s.watchPairs(session, peer, done)

	peer.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		s.log.Infof("ingest %s: %s", ing.Label, state)

		switch state {
		case webrtc.PeerConnectionStateConnected:
			s.mu.Lock()
			if s.currentLocked(session) {
				s.stats.Publishing(ing.ID)
			}
			s.mu.Unlock()
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			stopPairs()
			s.stopSession(session)
		default:
		}
	})
}

// watchPath reports candidate changes, including renomination without reconnecting.
func (s *Server) watchPath(ing config.Ingest, session *Session, peer *webrtc.PeerConnection) {
	transport := peer.SCTP().Transport().ICETransport()
	if transport == nil {
		return
	}

	transport.OnSelectedCandidatePairChange(func(pair *webrtc.ICECandidatePair) {
		if session.failover != nil {
			session.failover.selected.Store(pair)
		}
		if pair == nil || pair.Local == nil || pair.Remote == nil {
			return
		}

		path := describePair(pair)
		s.log.Infof("ingest %s: now on %s", ing.Label, path)
		s.mu.Lock()
		if s.currentLocked(session) {
			s.stats.Path(ing.ID, path)
		}
		s.mu.Unlock()
	})
}

// describePair labels the selected route for the dashboard.
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

// isPrivate includes loopback and link-local addresses in the local-network label.
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
	session, ok := s.session(resource)
	if !ok {
		return ErrNoSession
	}

	s.stopSession(session)

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
		if s.byIngest[session.IngestID] == resource {
			delete(s.byIngest, session.IngestID)
		}
		delete(s.sessions, resource)
	}
}

// The camera remains owned until relay/subscriber cleanup completes. A retry
// can wait in stopOnce, but no server mutex is held while closing subscribers.
func (s *Server) stopSession(session *Session) {
	session.stopOnce.Do(func() {
		s.mu.Lock()
		owned := s.currentLocked(session)
		session.ended = true
		s.mu.Unlock()
		if owned {
			s.stats.Stopped(session.IngestID)
			s.relay.Drop(session.IngestID)
			s.stopped(session.IngestID)
		}
		s.forget(session.Resource)
	})
}

func (s *Server) currentLocked(session *Session) bool {
	return !session.ended && s.byIngest[session.IngestID] == session.Resource &&
		s.sessions[session.Resource] == session
}

// Close ends every live session.
func (s *Server) Close() {
	s.mu.Lock()
	s.closed = true
	live := make([]*Session, 0, len(s.sessions))

	for _, session := range s.sessions {
		live = append(live, session)
	}

	s.mu.Unlock()

	for _, session := range live {
		s.stopSession(session)
		if err := session.peer.Close(); err != nil {
			s.log.Warnf("close session: %v", err)
		}
	}
}

// API exposes the shared WebRTC stack so egress does not build a second one.
func (s *Server) API() *webrtc.API {
	return s.api
}
