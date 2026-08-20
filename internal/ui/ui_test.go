// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package ui

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MarcFryd/wagaStrim/internal/config"
	"github.com/MarcFryd/wagaStrim/internal/stats"
	"github.com/pion/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testPort = 7330

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
	assert.Equal(t, http.StatusNotFound, get(t, "127.0.0.1:7330", "cross-site"))
	assert.Equal(t, http.StatusNotFound, get(t, "127.0.0.1:7330", "same-site"))
}

func TestThePageItselfIsServed(t *testing.T) {
	for _, host := range []string{"127.0.0.1:7330", "localhost:7330", "[::1]:7330"} {
		assert.Equal(t, http.StatusOK, get(t, host, "same-origin"), host)
	}

	// A typed URL, and anything that is not a browser at all.
	assert.Equal(t, http.StatusOK, get(t, "127.0.0.1:7330", "none"))
	assert.Equal(t, http.StatusOK, get(t, "127.0.0.1:7330", ""))
}
