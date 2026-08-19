// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

// Package signal is the public listener. It is deliberately not the same
// listener as the settings UI: everything here is reachable from the internet
// and gated on a key, and nothing here may expose settings.
package signal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/MarcFryd/wagaStrim/internal/config"
	"github.com/MarcFryd/wagaStrim/internal/egress"
	"github.com/MarcFryd/wagaStrim/internal/ingest"
	"github.com/pion/logging"
)

const (
	shutdownGrace = 5 * time.Second
	maxOfferBytes = 256 << 10
)

// Server routes WHIP over HTTP to the ingest package.
type Server struct {
	cfg    *config.Config
	log    logging.LeveledLogger
	whip   *ingest.Server
	whep   *egress.Server
	http   *http.Server
	listen string
}

// New wires the routes. The listener binds every interface, because a phone on
// cellular has to reach it.
func New(cfg *config.Config, log logging.LeveledLogger, whip *ingest.Server, whep *egress.Server) *Server {
	srv := &Server{
		cfg:    cfg,
		log:    log,
		whip:   whip,
		whep:   whep,
		listen: fmt.Sprintf(":%d", cfg.SignalPort),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /whip/{key}", srv.handlePublish)
	mux.HandleFunc("DELETE /whip/resource/{resource}", srv.handleTeardown)
	mux.HandleFunc("POST /whep/{key}", srv.handleSubscribe)
	mux.HandleFunc("DELETE /whep/resource/{resource}", srv.handleUnsubscribe)
	mux.HandleFunc("GET /player/{key}", srv.handlePlayer)

	srv.http = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	return srv
}

// Serve blocks until the context is canceled.
func (s *Server) Serve(ctx context.Context) error {
	var lcfg net.ListenConfig

	listener, err := lcfg.Listen(ctx, "tcp", s.listen)
	if err != nil {
		return fmt.Errorf("%w: signaling on %s: %w", ErrServe, s.listen, err)
	}

	errs := make(chan error, 1)

	go func() { errs <- s.http.Serve(listener) }()

	s.log.Infof("signaling on %s", listener.Addr())

	select {
	case err := <-errs:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}

		return fmt.Errorf("%w: %w", ErrServe, err)
	case <-ctx.Done():
		stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
		defer cancel()

		return s.http.Shutdown(stop)
	}
}

// negotiate reads an offer, hands it to whichever endpoint owns it, and writes
// the answer. WHIP and WHEP differ in the negotiator and the resource prefix.
func (s *Server) negotiate(
	wri http.ResponseWriter,
	req *http.Request,
	prefix string,
	run func(key, offer string) (string, string, error),
	refuse func(http.ResponseWriter, string, error),
) {
	offer, err := io.ReadAll(http.MaxBytesReader(wri, req.Body, maxOfferBytes))
	if err != nil {
		http.Error(wri, "offer too large", http.StatusRequestEntityTooLarge)

		return
	}

	key := req.PathValue("key")

	answer, resource, err := run(key, string(offer))
	if err != nil {
		refuse(wri, key, err)

		return
	}

	s.writeSDP(wri, answer, prefix+resource)
}

// writeSDP returns a WHIP or WHEP answer. The body comes from our own
// PeerConnection rather than the request, but taint analysis cannot see that;
// the vector it worries about is a browser sniffing application/sdp as HTML, so
// sniffing is refused outright instead of reasoning about provenance.
func (s *Server) writeSDP(wri http.ResponseWriter, answer, location string) {
	wri.Header().Set("content-type", "application/sdp")
	wri.Header().Set("x-content-type-options", "nosniff")
	wri.Header().Set("location", location)
	wri.WriteHeader(http.StatusCreated)

	// #nosec G705 -- SDP from our own PeerConnection, served nosniff as application/sdp.
	if _, err := wri.Write([]byte(answer)); err != nil {
		s.log.Warnf("write answer: %v", err)
	}
}

// release ends a session named by its resource id.
func release(wri http.ResponseWriter, req *http.Request, run func(string) error) {
	if err := run(req.PathValue("resource")); err != nil {
		http.Error(wri, "not found", http.StatusNotFound)

		return
	}

	wri.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePublish(wri http.ResponseWriter, req *http.Request) {
	s.negotiate(wri, req, "/whip/resource/", s.whip.Publish, s.reject)
}

func (s *Server) handleTeardown(wri http.ResponseWriter, req *http.Request) {
	release(wri, req, s.whip.Teardown)
}

func (s *Server) handleSubscribe(wri http.ResponseWriter, req *http.Request) {
	s.negotiate(wri, req, "/whep/resource/", s.whep.Subscribe, s.rejectSubscribe)
}

func (s *Server) handleUnsubscribe(wri http.ResponseWriter, req *http.Request) {
	release(wri, req, s.whep.Teardown)
}

// handlePlayer serves the page an OBS Browser Source points at. The key stays in
// the path so the page needs no configuration of its own, and the page is served
// from the public listener because OBS may be on another machine.
func (s *Server) handlePlayer(wri http.ResponseWriter, req *http.Request) {
	if _, role := s.cfg.Resolve(req.PathValue("key")); role != config.RoleReceiver {
		http.Error(wri, "not found", http.StatusNotFound)

		return
	}

	page, err := egress.PlayerPage()
	if err != nil {
		http.Error(wri, "not found", http.StatusNotFound)

		return
	}

	wri.Header().Set("content-type", "text/html; charset=utf-8")
	wri.Header().Set("cache-control", "no-store")

	if _, err := wri.Write(page); err != nil {
		s.log.Warnf("write player: %v", err)
	}
}

// rejectSubscribe mirrors reject, in the other direction.
func (s *Server) rejectSubscribe(wri http.ResponseWriter, _ string, cause error) {
	s.log.Warnf("subscribe rejected: %v", cause)

	switch {
	case errors.Is(cause, egress.ErrWrongRole):
		http.Error(wri,
			"This is the sender link, which belongs in Moblin. OBS needs the receiver link.",
			http.StatusBadRequest)
	case errors.Is(cause, egress.ErrNotReceiving):
		http.Error(wri,
			"This endpoint serves media. A publisher should use the sender link instead.",
			http.StatusBadRequest)
	case errors.Is(cause, egress.ErrOffline):
		http.Error(wri, "That camera is not streaming yet.", http.StatusServiceUnavailable)
	case errors.Is(cause, egress.ErrUnknownKey):
		http.Error(wri, "not found", http.StatusNotFound)
	default:
		http.Error(wri, "cannot negotiate", http.StatusBadRequest)
	}
}

// reject turns a failure into the most useful thing that can be said without
// telling an unauthenticated caller anything. A caller holding a real key for
// this ingest already knows the ingest exists, so naming the mix-up leaks
// nothing; everyone else gets an undifferentiated 404.
func (s *Server) reject(wri http.ResponseWriter, key string, cause error) {
	s.log.Warnf("publish rejected: %v", cause)

	switch {
	case errors.Is(cause, ingest.ErrWrongRole):
		http.Error(wri,
			"This is the receiver link, which belongs in OBS. Moblin needs the sender link.",
			http.StatusBadRequest)
	case errors.Is(cause, ingest.ErrNotSending):
		http.Error(wri,
			"This endpoint publishes media. An OBS receiver should use the receiver link instead.",
			http.StatusBadRequest)
	case errors.Is(cause, ingest.ErrAlreadyLive):
		http.Error(wri, "That camera already has a publisher.", http.StatusConflict)
	case errors.Is(cause, ingest.ErrUnknownKey):
		http.Error(wri, "not found", http.StatusNotFound)
	default:
		s.logKeyShape(key)
		http.Error(wri, "cannot negotiate", http.StatusBadRequest)
	}
}

// logKeyShape records only whether the key looked like one of ours, never the key.
func (s *Server) logKeyShape(key string) {
	switch {
	case strings.HasPrefix(key, config.SenderPrefix):
		s.log.Warnf("failed negotiation for a sender key")
	case strings.HasPrefix(key, config.ReceiverPrefix):
		s.log.Warnf("failed negotiation for a receiver key")
	default:
		s.log.Warnf("failed negotiation for an unrecognized key shape")
	}
}
