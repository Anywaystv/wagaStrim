// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

// Package control is the listener a deployment that owns the ingest list talks
// to. It is separate from the settings page because that page is loopback and
// unauthenticated, and separate from signaling because signaling is world open
// and nothing there may reach settings. It binds only where a token is
// configured, so a desktop install never grows this surface at all.
package control

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/MarcFryd/wagaStrim/internal/config"
	"github.com/MarcFryd/wagaStrim/internal/listen"
	"github.com/MarcFryd/wagaStrim/internal/stats"
	"github.com/pion/logging"
)

// maxBodyBytes is generous for a list of cameras and far below anything that
// could be used to make the daemon allocate.
const maxBodyBytes = 64 << 10

// Server applies an externally owned ingest list and reports what the cameras
// are doing.
type Server struct {
	cfg      *config.Config
	log      logging.LeveledLogger
	counters *stats.Registry
	http     *http.Server
	listen   string

	// revoke closes the live sessions of a camera whose keys moved, which is the
	// same teardown deleting one from the settings page performs.
	revoke func(ingestID string)
}

// New wires the routes. Binding happens in Serve, so a caller can decide not to.
func New(
	cfg *config.Config,
	log logging.LeveledLogger,
	counters *stats.Registry,
	revoke func(string),
) *Server {
	srv := &Server{
		cfg:      cfg,
		log:      log,
		counters: counters,
		listen:   net.JoinHostPort(cfg.ControlBind, fmt.Sprint(cfg.ControlPort)),
		revoke:   revoke,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("PUT /control/ingests", srv.authed(srv.handleIngests))
	mux.HandleFunc("GET /control/stats", srv.authed(srv.handleStats))

	srv.http = &http.Server{
		Handler:           listen.Guard(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	return srv
}

// Serve blocks until the context is canceled.
func (s *Server) Serve(ctx context.Context) error {
	return listen.Serve(ctx, s.log, s.http, s.listen, "control", s.cfg.TLSCert, s.cfg.TLSKey)
}

// authed rejects anything without the configured bearer token. Every failure is
// the same 404 as an unknown route, so the port says nothing about what it is.
func (s *Server) authed(next http.HandlerFunc) http.HandlerFunc {
	return func(wri http.ResponseWriter, req *http.Request) {
		// Inspect the socket peer, never a client-supplied forwarding header.
		peer, err := netip.ParseAddrPort(req.RemoteAddr)
		if err != nil || !s.allowedAddress(peer.Addr()) {
			http.Error(wri, "not found", http.StatusNotFound)

			return
		}
		offered := strings.TrimPrefix(req.Header.Get("authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(offered), []byte(s.cfg.ControlToken)) != 1 {
			s.log.Warnf("control request without a valid token: %s", req.URL.Path)
			http.Error(wri, "not found", http.StatusNotFound)

			return
		}

		next(wri, req)
	}
}

func (s *Server) allowedAddress(address netip.Addr) bool {
	address = address.Unmap()

	if address.IsLoopback() {
		return true
	}
	if address.IsPrivate() || address.IsLinkLocalUnicast() {
		return s.cfg.LANControlEnabled()
	}

	return address.IsGlobalUnicast() && s.cfg.RemoteControlEnabled()
}

// handleIngests replaces the whole camera list. It is declarative because the
// deployment that owns the list replays it after every change and after every
// machine replacement, and converging on a document is one code path where add,
// remove and rekey would be three.
func (s *Server) handleIngests(wri http.ResponseWriter, req *http.Request) {
	var body struct {
		Ingests []config.Ingest `json:"ingests"`
	}

	if err := decode(req, &body); err != nil {
		s.log.Warnf("control ingests: %v", err)
		http.Error(wri, "cannot read ingest list", http.StatusBadRequest)

		return
	}

	stale, err := s.cfg.ReplaceIngests(body.Ingests)
	if err != nil {
		s.log.Warnf("control ingests: %v", err)
		http.Error(wri, "ingest list was refused", http.StatusBadRequest)

		return
	}

	for _, id := range stale {
		s.revoke(id)
	}

	s.log.Infof("control set %d cameras, dropping %d live session(s)", len(body.Ingests), len(stale))
	wri.WriteHeader(http.StatusNoContent)
}

// handleStats reports every camera, so a deployment on another machine can see
// which ones have a publisher without reading the settings page.
func (s *Server) handleStats(wri http.ResponseWriter, _ *http.Request) {
	wri.Header().Set("content-type", "application/json")

	if err := json.NewEncoder(wri).Encode(s.counters.Report(s.cfg.IngestIDs())); err != nil {
		s.log.Warnf("control stats: %v", err)
	}
}

// decode reads a bounded JSON body and refuses fields this build does not know,
// so a typo in a provisioning template fails loudly instead of being ignored.
func decode(req *http.Request, into any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, req.Body, maxBodyBytes))
	dec.DisallowUnknownFields()

	if err := dec.Decode(into); err != nil {
		return fmt.Errorf("%w: %w", ErrBadRequest, err)
	}

	return nil
}
