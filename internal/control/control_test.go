// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package control

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

const token = "a-provisioned-token"

func testServer(t *testing.T) (*Server, *config.Config, *[]string) {
	t.Helper()

	// Load rather than a literal, because applying a list saves it, and a config
	// with nowhere to save to would fail for a reason this package is not testing.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)

	cfg, err := config.Load()
	require.NoError(t, err)

	// A compositor box: camera and player on one machine, so it lowers the floor
	// the IRL default exists for.
	cfg.ControlToken = token
	cfg.FloorMS = 300
	revoked := &[]string{}

	srv := New(cfg, logging.NewDefaultLoggerFactory().NewLogger("test"), stats.New(),
		func(id string) { *revoked = append(*revoked, id) })

	return srv, cfg, revoked
}

func put(t *testing.T, srv *Server, bearer, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/control/ingests", strings.NewReader(body))
	if bearer != "" {
		req.Header.Set("authorization", "Bearer "+bearer)
	}

	res := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(res, req)

	return res
}

// list is the document a deployment PUTs: one camera, keys it minted itself.
func list(suffix string) string {
	return `{"ingests":[{"id":"cam1","label":"Camera","senderKey":"s_` +
		strings.Repeat(suffix, 32) + `","receiverKey":"r_` + strings.Repeat(suffix, 32) +
		`","codecs":["h264"],"delayMs":300}]}`
}

// The token is the only thing standing between the internet and a rewrite of
// every key, so a wrong one and a missing one both look like an empty port.
func TestAWrongTokenLooksLikeNothingIsThere(t *testing.T) {
	srv, cfg, _ := testServer(t)

	assert.Equal(t, http.StatusNotFound, put(t, srv, "", list("a")).Code)
	assert.Equal(t, http.StatusNotFound, put(t, srv, "nearly-"+token, list("a")).Code)
	assert.Empty(t, cfg.List(), "a refused request must not have applied anything")
}

func TestASuppliedListIsApplied(t *testing.T) {
	srv, cfg, _ := testServer(t)

	require.Equal(t, http.StatusNoContent, put(t, srv, token, list("a")).Code)

	stored := cfg.List()
	require.Len(t, stored, 1)
	assert.Equal(t, "cam1", stored[0].ID)
	assert.Equal(t, 300, stored[0].DelayMS, "a deployment floor of its own is honored")
}

func TestASessionWhoseKeysMovedIsDropped(t *testing.T) {
	srv, _, revoked := testServer(t)

	require.Equal(t, http.StatusNoContent, put(t, srv, token, list("a")).Code)
	assert.Empty(t, *revoked)

	require.Equal(t, http.StatusNoContent, put(t, srv, token, list("b")).Code)
	assert.Equal(t, []string{"cam1"}, *revoked, "the phone holding the old key must be cut off")
}

func TestAMalformedListIsRefused(t *testing.T) {
	srv, cfg, _ := testServer(t)

	assert.Equal(t, http.StatusBadRequest, put(t, srv, token, `{"ingests":[{"id":"cam1"}]}`).Code)
	assert.Equal(t, http.StatusBadRequest, put(t, srv, token, `{"cameras":[]}`).Code,
		"an unknown field is a provisioning typo, not something to ignore")
	assert.Empty(t, cfg.List())
}

func TestStatsNeedTheTokenToo(t *testing.T) {
	srv, _, _ := testServer(t)
	require.Equal(t, http.StatusNoContent, put(t, srv, token, list("a")).Code)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/control/stats", nil)
	res := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(res, req)
	assert.Equal(t, http.StatusNotFound, res.Code)

	req.Header.Set("authorization", "Bearer "+token)
	res = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(res, req)

	assert.Equal(t, http.StatusOK, res.Code)
	assert.Contains(t, res.Body.String(), `"cam1"`, "every configured camera is reported")
	assert.NotContains(t, res.Body.String(), "s_", "a stats payload may never carry a key")
}
