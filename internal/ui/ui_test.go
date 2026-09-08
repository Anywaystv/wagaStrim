// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MarcFryd/wagaStrim/internal/config"
	"github.com/MarcFryd/wagaStrim/internal/stats"
	"github.com/pion/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testPort   = 7330
	sameOrigin = "same-origin"
	crossSite  = "cross-site"
)

func TestTokenBootstrapGuardsAndGuide(t *testing.T) {
	cfg := &config.Config{UIPort: testPort, ControlToken: "keep-existing-secret"}
	log := logging.NewDefaultLoggerFactory().NewLogger("test")
	srv, err := New(cfg, log, stats.New(), func(string) {}, func(string, int) {})
	require.NoError(t, err)
	for _, site := range []string{sameOrigin, crossSite, "same-site"} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
			"http://127.0.0.1:7330/api/control/token", nil)
		req.Header.Set("Sec-Fetch-Site", site)
		res := httptest.NewRecorder()
		srv.http.Handler.ServeHTTP(res, req)
		want := http.StatusNotFound
		if site == sameOrigin {
			want = http.StatusConflict
			assert.Equal(t, "no-store", res.Header().Get("Cache-Control"))
		}
		assert.Equal(t, want, res.Code)
		assert.NotContains(t, res.Body.String(), cfg.Token())
	}
	for _, path := range []string{"/", "/api-guide"} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://127.0.0.1:7330"+path, nil)
		res := httptest.NewRecorder()
		srv.http.Handler.ServeHTTP(res, req)
		require.Equal(t, http.StatusOK, res.Code)
		assert.NotContains(t, res.Body.String(), cfg.Token())
		assert.Contains(t, res.Body.String(), "API setup")
	}
}

func TestTokenBootstrapDoesNotClaimSuccessOnSaveFailure(t *testing.T) {
	cfg := &config.Config{UIPort: testPort}
	log := logging.NewDefaultLoggerFactory().NewLogger("test")
	srv, err := New(cfg, log, stats.New(), func(string) {}, func(string, int) {})
	require.NoError(t, err)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
		"http://127.0.0.1:7330/api/control/token", nil)
	res := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(res, req)
	assert.Equal(t, http.StatusInternalServerError, res.Code)
	assert.Empty(t, cfg.Token())
}

func TestLANControlToggleRendersSavedState(t *testing.T) {
	for _, on := range []bool{true, false} {
		cfg := &config.Config{UIPort: testPort, ControlToken: "configured", ControlLAN: &on}
		cfg.ControlRemote = &on
		log := logging.NewDefaultLoggerFactory().NewLogger("test")
		srv, err := New(cfg, log, stats.New(), func(string) {}, func(string, int) {})
		require.NoError(t, err)
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://127.0.0.1:7330/", nil)
		res := httptest.NewRecorder()
		srv.http.Handler.ServeHTTP(res, req)
		require.Equal(t, http.StatusOK, res.Code)
		assert.Equal(t, 1, strings.Count(res.Body.String(), `id="control-lan"`))
		assert.Equal(t, on, strings.Contains(res.Body.String(), `id="control-lan" checked`))
		assert.Equal(t, 1, strings.Count(res.Body.String(), `id="control-remote"`))
		assert.Equal(t, on, strings.Contains(res.Body.String(), `id="control-remote" checked`))
	}
}

func TestLANControlToggleRejectsCrossSiteAndInvalidBody(t *testing.T) {
	cfg := &config.Config{UIPort: testPort}
	log := logging.NewDefaultLoggerFactory().NewLogger("test")
	srv, err := New(cfg, log, stats.New(), func(string) {}, func(string, int) {})
	require.NoError(t, err)
	for _, site := range []string{sameOrigin, crossSite} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
			"http://127.0.0.1:7330/api/control/lan", strings.NewReader(`{}`))
		req.Header.Set("Sec-Fetch-Site", site)
		res := httptest.NewRecorder()
		srv.http.Handler.ServeHTTP(res, req)
		if site == sameOrigin {
			assert.Equal(t, http.StatusBadRequest, res.Code)
		} else {
			assert.Equal(t, http.StatusNotFound, res.Code)
		}
		assert.True(t, cfg.LANControlEnabled())
	}
}

// get drives the served handler rather than a bare mux, because the guard being
// tested lives between them and a refactor that dropped it must fail here.
func get(t *testing.T, host, site string) int {
	t.Helper()

	cfg := &config.Config{UIPort: testPort}
	log := logging.NewDefaultLoggerFactory().NewLogger("test")

	srv, err := New(cfg, log, stats.New(), func(string) {}, func(string, int) {})
	require.NoError(t, err)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", nil)
	req.Host = host

	if site != "" {
		req.Header.Set("sec-fetch-site", site)
	}

	res := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(res, req)

	return res.Code
}

// The settings page renders both stream links into the HTML, so a name an
// author points at 127.0.0.1 would let their page read the keys straight out of
// it. Binding to loopback does not stop that; refusing the Host does.
func TestAReboundHostIsRefused(t *testing.T) {
	assert.Equal(t, http.StatusNotFound, get(t, "settings.example:7330", ""))
}

// The mutating endpoints take JSON but never check the content type, so a
// cross-site text/plain post is not preflighted and would otherwise arrive.
func TestACrossSiteRequestIsRefused(t *testing.T) {
	assert.Equal(t, http.StatusNotFound, get(t, "127.0.0.1:7330", crossSite))
	assert.Equal(t, http.StatusNotFound, get(t, "127.0.0.1:7330", "same-site"))
}

func TestThePageItselfIsServed(t *testing.T) {
	for _, host := range []string{"127.0.0.1:7330", "localhost:7330", "[::1]:7330"} {
		assert.Equal(t, http.StatusOK, get(t, host, sameOrigin), host)
	}

	// A typed URL, and anything that is not a browser at all.
	assert.Equal(t, http.StatusOK, get(t, "127.0.0.1:7330", "none"))
	assert.Equal(t, http.StatusOK, get(t, "127.0.0.1:7330", ""))
}

func TestCameraSettingsCollapseIndependently(t *testing.T) {
	cfg := &config.Config{UIPort: testPort, Ingests: []config.Ingest{
		{ID: "cam1", Label: "Phone", DelayMS: 2000},
		{ID: "cam2", Label: "Drone", DelayMS: 3000, SyncGroup: "outside"},
	}}
	log := logging.NewDefaultLoggerFactory().NewLogger("test")
	srv, err := New(cfg, log, stats.New(), func(string) {}, func(string, int) {})
	require.NoError(t, err)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://127.0.0.1:7330/", nil)
	res := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(res, req)
	require.Equal(t, http.StatusOK, res.Code)
	for _, camera := range cfg.Ingests {
		_, card, found := strings.Cut(res.Body.String(), `data-id="`+camera.ID+`">`)
		require.True(t, found)
		card, _, found = strings.Cut(card, "</section>")
		require.True(t, found)
		header, settings, found := strings.Cut(card, `<details class="camera-settings">`)
		require.True(t, found, "each camera starts collapsed without grouping other cameras")
		assert.Contains(t, header, `data-label="`+camera.ID+`"`)
		assert.Contains(t, header, "data-status")
		assert.Contains(t, header, "data-stats")
		assert.Contains(t, settings, `for `+camera.Label+`</span></summary>`)
		for _, field := range []string{"send-", "recv-", "delay-", "group-"} {
			assert.Contains(t, settings, `id="`+field+camera.ID+`"`)
		}
		assert.Contains(t, settings, `data-codecs="`+camera.ID+`"`)
		assert.Contains(t, settings, `data-reset-key="`+camera.ID+`"`)
		assert.True(t, strings.HasSuffix(strings.TrimSpace(settings), "</details>"))
	}
}

func TestResetKeyRejectsCrossSiteRequests(t *testing.T) {
	cfg := &config.Config{UIPort: testPort}
	log := logging.NewDefaultLoggerFactory().NewLogger("test")
	revoked := false
	srv, err := New(cfg, log, stats.New(), func(string) { revoked = true }, func(string, int) {})
	require.NoError(t, err)
	for _, site := range []string{sameOrigin, crossSite} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
			"http://127.0.0.1:7330/api/ingests/reset-key", strings.NewReader(`{}`))
		req.Header.Set("Sec-Fetch-Site", site)
		res := httptest.NewRecorder()
		srv.http.Handler.ServeHTTP(res, req)
		want := http.StatusBadRequest
		if site != sameOrigin {
			want = http.StatusNotFound
		}
		assert.Equal(t, want, res.Code)
	}
	assert.False(t, revoked)
}
