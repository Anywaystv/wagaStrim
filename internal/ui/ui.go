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
	"net/http/pprof"
	"slices"
	"time"

	"github.com/MarcFryd/wagaStrim/internal/autostart"
	"github.com/MarcFryd/wagaStrim/internal/config"
	"github.com/MarcFryd/wagaStrim/internal/ingest"
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

	// Set by main. Deleting a camera has to disconnect whatever is using it, and
	// a delay change has to reach the buffers already running.
	revoke   func(ingestID string)
	retarget func(ingestID string, delayMS int)
}

// pageData is what the template sees.
type pageData struct {
	Ingests      []cameraView
	SenderBase   string
	ReceiverBase string
}

// cameraView pairs a camera with the target it actually plays out at. In a sync
// group that is the slowest member's, not its own, and showing only the stored
// value would put a number on screen that is not the one in effect.
type cameraView struct {
	config.Ingest
	Effective int
	Codecs    []codecChoice
}

// codecChoice is one toggle plus the caveat that applies to it. Both caveats
// describe combinations that negotiate successfully and then show nothing, so
// they belong next to the control, not in a README.
type codecChoice struct {
	Name   string
	Label  string
	On     bool
	Caveat string
}

func codecChoices(on []string) []codecChoice {
	labels := map[string]string{
		config.CodecH264: "H.264",
		config.CodecH265: "H.265",
		config.CodecAV1:  "AV1",
	}
	caveats := map[string]string{
		config.CodecH265: "Needs OBS started with --enable-features=WebRtcAllowH265Receive. " +
			"Its bundled Chromium is 133, which has the flag but not the default.",
		config.CodecAV1: "No iPhone can encode AV1, so Moblin will never pick it. " +
			"For a desktop or OBS publisher only.",
	}

	out := make([]codecChoice, 0, len(config.AllCodecs()))

	for _, name := range config.AllCodecs() {
		out = append(out, codecChoice{
			Name:   name,
			Label:  labels[name],
			On:     slices.Contains(on, name),
			Caveat: caveats[name],
		})
	}

	return out
}

// New compiles the page and wires the routes.
func New(
	cfg *config.Config,
	log logging.LeveledLogger,
	counters *stats.Registry,
	revoke func(string),
	retarget func(string, int),
) (*Server, error) {
	tpl, err := template.ParseFS(assets, "web/index.html")
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrParseTemplate, err)
	}

	srv := &Server{cfg: cfg, log: log, tpl: tpl, counters: counters, revoke: revoke, retarget: retarget}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", srv.handleHealth)
	mux.HandleFunc("GET /{$}", srv.handlePage)
	mux.HandleFunc("POST /api/ingests", srv.handleAdd)
	mux.HandleFunc("POST /api/ingests/remove", srv.handleRemove)
	mux.HandleFunc("POST /api/ingests/delay", srv.handleDelay)
	mux.HandleFunc("GET /api/stats", srv.handleStats)
	mux.HandleFunc("POST /api/ingests/group", srv.handleGroup)
	mux.HandleFunc("POST /api/ingests/label", srv.handleLabel)
	mux.HandleFunc("POST /api/ingests/codecs", srv.handleCodecs)
	mux.HandleFunc("GET /api/reachability", srv.handleReachability)
	mux.HandleFunc("GET /api/autostart", srv.handleAutostart)
	mux.HandleFunc("POST /api/autostart", srv.handleSetAutostart)

	// Profiling lives on the loopback listener and nowhere else. It exposes
	// memory contents and can be made to burn a core, so it must never be
	// reachable from the internet the way the signaling listener is.
	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
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
	report.SocketNote = socketNote()

	// Remember a discovered address so the links stop reading as a placeholder.
	if report.PublicHost != "" {
		if err := s.cfg.SetPublicHost(report.PublicHost); err != nil {
			s.log.Warnf("save public host: %v", err)
		}
	}

	s.writeJSON(wri, report)
}

func (s *Server) handleAutostart(wri http.ResponseWriter, req *http.Request) {
	s.writeJSON(wri, autostart.Status(req.Context()))
}

func (s *Server) handleSetAutostart(wri http.ResponseWriter, req *http.Request) {
	var body struct {
		On bool `json:"on"`
	}

	if err := decode(req, &body); err != nil {
		s.fail(wri, http.StatusBadRequest, err)

		return
	}

	act := autostart.Disable
	if body.On {
		act = autostart.Enable
	}

	if err := act(req.Context()); err != nil {
		s.fail(wri, http.StatusInternalServerError, err)

		return
	}

	// The stored flag is only what the page renders on load. The platform is the
	// authority, and Status re-reads it, so the two cannot drift apart.
	if err := s.cfg.SetAutostart(body.On); err != nil {
		s.log.Warnf("record autostart: %v", err)
	}

	s.writeJSON(wri, autostart.Status(req.Context()))
}

// socketNote reports the receive buffer the kernel actually grants. A clamped
// buffer drops packets before the application sees them, and nothing logs it,
// so the number has to be visible rather than assumed.
func socketNote() string {
	const want = 8 << 20

	granted, err := ingest.ProbeSocketBuffer(want)
	if err != nil || granted == 0 {
		return ""
	}

	if granted >= want {
		return fmt.Sprintf("Socket receive buffer %d MB, as requested.", granted>>20)
	}

	return fmt.Sprintf(
		"Socket receive buffer is %d KB, but %d MB was requested. The kernel capped it, "+
			"which drops packets under burst before this app sees them. Raise net.core.rmem_max "+
			"on Linux or kern.ipc.maxsockbuf on macOS.",
		granted>>10, want>>20)
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

	cams := s.cfg.List()
	views := make([]cameraView, len(cams))

	for idx, cam := range cams {
		views[idx] = cameraView{
			Ingest:    cam,
			Effective: s.cfg.EffectiveDelay(cam.ID),
			Codecs:    codecChoices(cam.Codecs),
		}
	}

	data := pageData{
		Ingests:    views,
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

// mutate decodes a request body and runs a change against the config. All five
// mutating endpoints were the same decode, the same two error shapes, and the
// same empty success.
func mutate[T any](s *Server, wri http.ResponseWriter, req *http.Request, run func(T) error) {
	var body T

	if err := decode(req, &body); err != nil {
		s.fail(wri, http.StatusBadRequest, err)

		return
	}

	if err := run(body); err != nil {
		s.fail(wri, http.StatusNotFound, err)

		return
	}

	wri.WriteHeader(http.StatusNoContent)
}

type idBody struct {
	ID string `json:"id"`
}

func (s *Server) handleAdd(wri http.ResponseWriter, req *http.Request) {
	mutate(s, wri, req, func(body struct {
		Label string `json:"label"`
	},
	) error {
		_, err := s.cfg.AddIngest(body.Label)

		return err
	})
}

func (s *Server) handleRemove(wri http.ResponseWriter, req *http.Request) {
	mutate(s, wri, req, func(body idBody) error {
		if err := s.cfg.RemoveIngest(body.ID); err != nil {
			return err
		}

		s.revoke(body.ID)

		return nil
	})
}

func (s *Server) handleDelay(wri http.ResponseWriter, req *http.Request) {
	mutate(s, wri, req, func(body struct {
		idBody
		DelayMS int `json:"delayMs"`
	},
	) error {
		if err := s.cfg.SetDelay(body.ID, body.DelayMS); err != nil {
			return err
		}

		s.applyDelay(body.ID)

		return nil
	})
}

func (s *Server) handleGroup(wri http.ResponseWriter, req *http.Request) {
	mutate(s, wri, req, func(body struct {
		idBody
		Group string `json:"group"`
	},
	) error {
		// Peers of the group being left also need retargeting: losing the slowest
		// member should let the rest speed back up.
		former := s.cfg.GroupPeers(body.ID)

		if err := s.cfg.SetSyncGroup(body.ID, body.Group); err != nil {
			return err
		}

		for _, peer := range former {
			s.retarget(peer, s.cfg.EffectiveDelay(peer))
		}

		s.applyDelay(body.ID)

		return nil
	})
}

func (s *Server) handleLabel(wri http.ResponseWriter, req *http.Request) {
	mutate(s, wri, req, func(body struct {
		idBody
		Label string `json:"label"`
	},
	) error {
		return s.cfg.SetLabel(body.ID, body.Label)
	})
}

func (s *Server) handleCodecs(wri http.ResponseWriter, req *http.Request) {
	mutate(s, wri, req, func(body struct {
		idBody
		Codecs []string `json:"codecs"`
	},
	) error {
		if err := s.cfg.SetCodecs(body.ID, body.Codecs); err != nil {
			return err
		}

		// A codec change only takes effect on the next connection, so drop the
		// current publisher rather than leaving the old set running behind a UI
		// that says otherwise.
		s.revoke(body.ID)

		return nil
	})
}

// applyDelay pushes the effective target to every camera sharing this one's
// group, since raising one member raises the whole group.
func (s *Server) applyDelay(id string) {
	for _, peer := range s.cfg.GroupPeers(id) {
		s.retarget(peer, s.cfg.EffectiveDelay(peer))
	}
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
