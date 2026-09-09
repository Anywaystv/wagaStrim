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
	"sync/atomic"
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

// add records a packet and returns the running total, so the caller reporting
// it does not read the counter back through a second lock.
func (s *Session) add(count int) uint64 {
	return s.bytes.Add(uint64(count)) //nolint:gosec // count comes from a read length and is never negative.
}

// Server holds the shared WebRTC stack and the live sessions.
type Server struct {
	cfg    *config.Config
	log    logging.LeveledLogger
	api    *webrtc.API
	engine *webrtc.SettingEngine
	relay  *relay.Relay
	stats  *stats.Registry

	// stopped disconnects the subscribers of a publisher that has ended. A
	// subscriber is bound to the track object it was handed, and the publisher
	// gets a new one when it comes back, so a subscriber left attached sits
	// there connected and frozen for good. The player page reconnects itself.
	stopped func(ingestID string)

	mu       sync.Mutex
	sessions map[string]*Session
	byIngest map[string]string
	pending  map[string]bool
	closed   bool
}

// NewServer builds the WebRTC stack once and shares it across sessions.
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

// buildAPI assembles a WebRTC stack offering exactly the named video codecs.
// One is built per camera, because the toggles decide what the answer contains
// and the MediaEngine is what carries that decision.
func buildAPI(engine *webrtc.SettingEngine, codecs []string, arrival *arrivalFeedback) (*webrtc.API, error) {
	media := &webrtc.MediaEngine{}
	if err := registerCodecs(media, codecs); err != nil {
		return nil, err
	}

	// Reports and TWCC, but deliberately not RegisterDefaultInterceptors: that
	// helper also calls ConfigureNack, and the pair added below would then be the
	// second generator and the second responder in the chain rather than the
	// only ones. Two responders answer the same NACK, so every packet a receiver
	// asked for was sent to it twice -- measured on a live camera as 2.6 sends
	// per packet, 16 Mbps of egress for a 6 Mbps stream, video only, because
	// NACK is not negotiated for audio. Configuring the two halves by hand is
	// what keeps nackHistory below meaningful: the helper's own pair is fixed at
	// pion's default depth.
	registry := &interceptor.Registry{}
	if err := webrtc.ConfigureRTCPReports(registry); err != nil {
		return nil, fmt.Errorf("%w: rtcp reports: %w", ErrBuildAPI, err)
	}

	if err := configureArrivalFeedback(media, registry, arrival); err != nil {
		return nil, fmt.Errorf("%w: twcc: %w", ErrBuildAPI, err)
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

	// Outbound, like the responder above: a subscriber is told how much to hold
	// before it plays anything. See playout.go for why the page cannot be the
	// only place that asks.
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

// registerCodecs registers everything the relay can carry. Which of them an
// ingest actually offers is decided per camera when the answer is built.
//
// H.265 and AV1 sit on pion's own default payload types; H.264 is on 96, which
// pion gives to VP8 and which is what the phone apps tested here send. None of
// it is load bearing, because an answer carries the payload types the offer
// named, not these.
//
// The fmtp lines are the same story and it is worth saying out loud, because
// Opus below carries none and that reads like an oversight against pion's own
// default of "minptime=10;useinbandfec=1". An answer's fmtp also comes from the
// offer, and wagaStrim only ever answers, so what is written here never reaches
// the wire. Adding useinbandfec here would not switch in-band FEC on: the
// publisher already sees it, because it is the parameter it sent us. Measured
// both ways on Publish, 2026-08-30.
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

	// The camera's own codec set, not the server's. A toggle only means anything
	// if it changes what the answer offers.
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

	// The resource id is minted before the callbacks are wired, because they
	// name the session by it. Filling it in afterwards left a window where a
	// connection that failed early forgot a session called nothing, and the
	// ingest stayed marked as having a publisher.
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

// clearPrevious makes room for a publisher on an ingest that already has one.
//
// A phone that loses its link does not tell us; ICE takes tens of seconds to
// call the old session failed, and the phone is retrying long before that. A
// flat refusal for that whole window is the reconnect a streamer notices, so a
// session that is no longer connected is ended here and the new offer proceeds.
// A session that is genuinely still carrying media is not: two publishers on one
// camera would fight over it, and the second one is a mistake worth naming.
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
func (s *Server) drain(ing config.Ingest, session *Session, peer *webrtc.PeerConnection, track *webrtc.TrackRemote) {
	s.log.Infof("ingest %s: track %s %s", ing.Label, track.Kind(), track.Codec().MimeType)
	s.mu.Lock()
	if !s.currentLocked(session) {
		s.mu.Unlock()

		return
	}

	// What the publisher settled on, discovered from the negotiation rather than
	// assumed from the toggles. Stored as the label a person reads, like every
	// other string in a snapshot. Only the video track has one worth naming.
	if track.Kind() == webrtc.RTPCodecTypeVideo {
		s.stats.Codec(ing.ID, config.CodecLabelOf(track.Codec().MimeType))
	}

	// Video only. A PLI naming an audio SSRC asks for a picture from a stream
	// that has none, and the buffer below calls this on every drift correction.
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

	buf := relay.NewBuffer(
		time.Duration(s.cfg.EffectiveDelay(ing.ID))*time.Millisecond,
		track.Codec().ClockRate,
		track.Codec().MimeType,
		askKeyframe,
	)
	defer buf.Close()

	s.relay.Track(ing.ID, buf)
	s.mu.Unlock()
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

		total := session.add(pkt.MarshalSize())
		buf.Push(pkt)

		if track.Kind() == webrtc.RTPCodecTypeVideo {
			late, dropped := buf.Stats()
			s.mu.Lock()
			if s.currentLocked(session) {
				s.stats.Observe(ing.ID, total, late, dropped)
			}
			s.mu.Unlock()
		}
	}
}

// requestKeyframe sends a PLI naming a publisher's video SSRC.
func (s *Server) requestKeyframe(ing config.Ingest, peer *webrtc.PeerConnection, ssrc webrtc.SSRC) {
	err := peer.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: uint32(ssrc)}})
	if err != nil {
		s.log.Warnf("ingest %s: keyframe request: %v", ing.Label, err)
	}
}

// drainRTCP consumes the publisher's RTCP. Nothing acts on it here, but pion
// only moves RTCP when somebody reads it, and the report interceptor is what
// reads through this call: unread, it never sees a Sender Report, so every
// Receiver Report we send the phone carries a zero last-SR and zero delay, and
// the phone cannot measure the round trip it is adapting its bitrate against.
func drainRTCP(receiver *webrtc.RTPReceiver) {
	buf := make([]byte, 1500)

	for {
		if _, _, err := receiver.Read(buf); err != nil {
			return
		}
	}
}

// watch wires the callbacks that track a publisher's life.
func (s *Server) watch(ing config.Ingest, session *Session, peer *webrtc.PeerConnection) {
	peer.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		go drainRTCP(receiver)

		s.drain(ing, session, peer, track)
	})

	s.watchPath(ing, session, peer)

	// Failed and Closed can both arrive, so the stop signal has to tolerate
	// being fired twice. pion happens to serialize these callbacks today, which
	// is not a property worth depending on.
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

// watchPath reports which candidate pair is carrying the stream. Renomination
// moves a phone from Wi-Fi to cellular without a reconnect, and from outside
// the process that is indistinguishable from nothing happening, so the move has
// to be surfaced or the feature is invisible.
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
