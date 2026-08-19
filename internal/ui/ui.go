// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

// Package ui serves the settings page on loopback. It is deliberately separate
// from the signaling listener: this one is unauthenticated and must never be
// reachable from outside the machine.
package ui

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net"
	"net/http"
	"time"

	"github.com/MarcFryd/wagaStrim/internal/config"
	"github.com/MarcFryd/wagaStrim/internal/reach"
	"github.com/MarcFryd/wagaStrim/internal/stats"
	"github.com/pion/logging"
)

//go:embed all:web
var assets embed.FS

const shutdownGrace = 5 * time.Second

// Server renders and mutates the config over HTTP.
type Server struct {
	cfg      *config.Config
	log      logging.LeveledLogger
	tpl      *template.Template
	http     *http.Server
	counters *stats.Registry
}

// pageData is what the template sees.
type pageData struct {
	Ingests      []config.Ingest
	SenderBase   string
	ReceiverBase string
}

// New compiles the page and wires the routes.
func New(cfg *config.Config, log logging.LeveledLogger, counters *stats.Registry) (*Server, error) {
	tpl, err := template.ParseFS(assets, "web/index.html")
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrParseTemplate, err)
	}

	srv := &Server{cfg: cfg, log: log, tpl: tpl, counters: counters}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", srv.handleHealth)
	mux.HandleFunc("GET /{$}", srv.handlePage)
	mux.HandleFunc("POST /api/ingests", srv.handleAdd)
	mux.HandleFunc("POST /api/ingests/remove", srv.handleRemove)
	mux.HandleFunc("POST /api/ingests/delay", srv.handleDelay)
	mux.HandleFunc("GET /api/stats", srv.handleStats)
	mux.HandleFunc("GET /api/reachability", srv.handleReachability)
	static, err := fs.Sub(assets, "web")
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrEmbeddedAssets, err)
	}

	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))

	srv.http = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	return srv, nil
}

// Addr is the loopback URL a browser or the tray should open.
func (s *Server) Addr() string {
	return fmt.Sprintf("http://127.0.0.1:%d", s.cfg.UIPort)
}

// Serve blocks until the context is canceled.
func (s *Server) Serve(ctx context.Context) error {
	addr := fmt.Sprintf("127.0.0.1:%d", s.cfg.UIPort)

	var lcfg net.ListenConfig

	listener, err := lcfg.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("%w: ui on %s: %w", ErrServe, addr, err)
	}

	errs := make(chan error, 1)

	go func() { errs <- s.http.Serve(listener) }()

	s.log.Infof("settings on %s", s.Addr())

	select {
	case err := <-errs:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}

		return fmt.Errorf("%w: ui: %w", ErrServe, err)
	case <-ctx.Done():
		stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
		defer cancel()

		return s.http.Shutdown(stop)
	}
}

func (s *Server) handleStats(wri http.ResponseWriter, _ *http.Request) {
	ingests := s.cfg.List()
	out := make(map[string]stats.Snapshot, len(ingests))

	for idx := range ingests {
		out[ingests[idx].ID] = s.counters.Of(ingests[idx].ID)
	}

	s.writeJSON(wri, out)
}

// handleReachability runs a STUN lookup on demand rather than at startup, so a
// machine with no internet still boots and serves its settings page.
func (s *Server) handleReachability(wri http.ResponseWriter, req *http.Request) {
	report := reach.Look(req.Context(), s.cfg.MediaPort, s.cfg.SignalPort)

	// Remember a discovered address so the links stop reading as a placeholder.
	if report.PublicHost != "" {
		if err := s.cfg.SetPublicHost(report.PublicHost); err != nil {
			s.log.Warnf("save public host: %v", err)
		}
	}

	s.writeJSON(wri, report)
}

func (s *Server) writeJSON(wri http.ResponseWriter, body any) {
	wri.Header().Set("content-type", "application/json")

	if err := json.NewEncoder(wri).Encode(body); err != nil {
		s.log.Warnf("encode response: %v", err)
	}
}

func (s *Server) handleHealth(wri http.ResponseWriter, _ *http.Request) {
	wri.Header().Set("content-type", "text/plain")
	s.write(wri, []byte("ok\n"))
}

func (s *Server) handlePage(wri http.ResponseWriter, _ *http.Request) {
	host := s.cfg.Host()
	if host == "" {
		host = "your-public-address"
	}

	data := pageData{
		Ingests:    s.cfg.Ingests,
		SenderBase: fmt.Sprintf("http://%s:%d/whip/", host, s.cfg.SignalPort),
		// The Browser Source path is the default, so the receiver link is the
		// player page rather than the raw WHEP endpoint. A WHEP client can still
		// reach /whep/<same key> directly.
		ReceiverBase: fmt.Sprintf("http://%s:%d/player/", host, s.cfg.SignalPort),
	}

	wri.Header().Set("content-type", "text/html; charset=utf-8")

	if err := s.tpl.Execute(wri, data); err != nil {
		s.log.Errorf("render: %v", err)
	}
}

func (s *Server) handleAdd(wri http.ResponseWriter, req *http.Request) {
	var body struct {
		Label string `json:"label"`
	}

	if err := decode(req, &body); err != nil {
		s.fail(wri, http.StatusBadRequest, err)

		return
	}

	if _, err := s.cfg.AddIngest(body.Label); err != nil {
		s.fail(wri, http.StatusInternalServerError, err)

		return
	}

	wri.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRemove(wri http.ResponseWriter, req *http.Request) {
	var body struct {
		ID string `json:"id"`
	}

	if err := decode(req, &body); err != nil {
		s.fail(wri, http.StatusBadRequest, err)

		return
	}

	if err := s.cfg.RemoveIngest(body.ID); err != nil {
		s.fail(wri, http.StatusNotFound, err)

		return
	}

	wri.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDelay(wri http.ResponseWriter, req *http.Request) {
	var body struct {
		ID      string `json:"id"`
		DelayMS int    `json:"delayMs"`
	}

	if err := decode(req, &body); err != nil {
		s.fail(wri, http.StatusBadRequest, err)

		return
	}

	if err := s.cfg.SetDelay(body.ID, body.DelayMS); err != nil {
		s.fail(wri, http.StatusNotFound, err)

		return
	}

	wri.WriteHeader(http.StatusNoContent)
}

// fail logs the detail and returns only a status to the caller. Config paths and
// ingest identifiers stay out of the response body.
func (s *Server) fail(wri http.ResponseWriter, status int, err error) {
	s.log.Warnf("request failed: %v", err)
	http.Error(wri, http.StatusText(status), status)
}

func (s *Server) write(wri http.ResponseWriter, buf []byte) {
	if _, err := wri.Write(buf); err != nil {
		s.log.Warnf("write response: %v", err)
	}
}

func decode(req *http.Request, into any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, req.Body, 4096))
	dec.DisallowUnknownFields()

	if err := dec.Decode(into); err != nil {
		return fmt.Errorf("%w: %w", ErrBadRequest, err)
	}

	return nil
}
