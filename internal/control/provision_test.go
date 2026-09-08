// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package control

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MarcFryd/wagaStrim/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func provisioningRequest(t *testing.T, srv *Server, method, credential, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, "/control/ingests", strings.NewReader(body))
	req.RemoteAddr = loopbackPeer
	req.Header.Set("Authorization", credential)
	req.Host = "attacker.example"
	res := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(res, req)

	return res
}

func TestCreateAndRetrieveCameraLinks(t *testing.T) {
	srv, cfg, _ := testServer(t)
	require.NoError(t, cfg.SetPublicHost("192.168.68.92"))
	created := provisioningRequest(t, srv, http.MethodPost, "Bearer "+token, `{"label":" Phone "}`)
	require.Equal(t, http.StatusCreated, created.Code)
	assert.Equal(t, "no-store", created.Header().Get("Cache-Control"))
	var camera cameraResponse
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &camera))
	assert.Equal(t, "Phone", camera.Label)
	assert.Equal(t, config.DelayDefaultMS, camera.DelayMS)
	assert.Equal(t, "whip://192.168.68.92:7331/whip/"+camera.SenderKey, camera.Links.Moblin)
	assert.Equal(t, "http://192.168.68.92:7331/whip/"+camera.SenderKey, camera.Links.WHIP)
	assert.Equal(t, "http://192.168.68.92:7331/whep/"+camera.ReceiverKey, camera.Links.WHEP)
	assert.Equal(t, "http://192.168.68.92:7331/player/"+camera.ReceiverKey, camera.Links.Player)
	_, role := cfg.Resolve(camera.SenderKey)
	assert.Equal(t, config.RoleSender, role)
	_, role = cfg.Resolve(camera.ReceiverKey)
	assert.Equal(t, config.RoleReceiver, role)
	listed := provisioningRequest(t, srv, http.MethodGet, "Bearer "+token, "")
	require.Equal(t, http.StatusOK, listed.Code)
	assert.Contains(t, listed.Body.String(), camera.SenderKey)
	assert.Contains(t, listed.Body.String(), camera.Links.Player)
	assert.NotContains(t, listed.Body.String(), token)
	second := provisioningRequest(t, srv, http.MethodPost, "Bearer "+token, `{"label":"Phone"}`)
	require.Equal(t, http.StatusCreated, second.Code)
	assert.NotContains(t, second.Body.String(), camera.SenderKey)
	assert.NotContains(t, second.Body.String(), camera.ReceiverKey)
}

func TestProvisioningRejectsInvalidInputAndCredentials(t *testing.T) {
	srv, cfg, _ := testServer(t)
	for _, credential := range []string{"", token, "Bearer wrong"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			res := provisioningRequest(t, srv, method, credential, `{"label":"Phone"}`)
			assert.Equal(t, http.StatusNotFound, res.Code)
		}
	}
	for _, body := range []string{
		`{}`, `null`, `{"label":" "}`, `{"unknown":1}`,
		`{"label":"Phone"} {}`, `{"label":"` + strings.Repeat("x", 49) + `"}`,
		`{"label":"` + strings.Repeat("x", maxBodyBytes) + `"}`,
	} {
		res := provisioningRequest(t, srv, http.MethodPost, "Bearer "+token, body)
		assert.Equal(t, http.StatusBadRequest, res.Code)
	}
	assert.Empty(t, cfg.List())
	cfg.ControlToken = ""
	res := provisioningRequest(t, srv, http.MethodGet, "Bearer ", "")
	assert.Equal(t, http.StatusNotFound, res.Code)
}

func TestProvisioningLinkOrigins(t *testing.T) {
	srv, cfg, _ := testServer(t)
	camera := config.Ingest{SenderKey: "sender", ReceiverKey: "receiver"}
	assert.Equal(t, "http://127.0.0.1:7331/player/receiver", srv.cameraResponse(camera).Links.Player)
	require.NoError(t, cfg.SetPublicHost("2001:db8::1"))
	cfg.TLSCert, cfg.TLSKey = "cert", "key"
	assert.Equal(t, "whips://[2001:db8::1]:7331/whip/sender", srv.cameraResponse(camera).Links.Moblin)
	cfg.PublicURL = "https://stream.example.com/"
	links := srv.cameraResponse(camera).Links
	assert.Equal(t, "https://stream.example.com/whip/sender", links.WHIP)
	assert.Equal(t, "https://stream.example.com/whep/receiver", links.WHEP)
}

func TestResetKeyRequiresAuthenticationAndRevokesSessions(t *testing.T) {
	srv, cfg, revoked := testServer(t)
	before, err := cfg.AddIngest("Phone")
	require.NoError(t, err)
	path := "/control/ingests/" + before.ID + "/reset-key"
	for _, credential := range []string{wrongToken, token} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, nil)
		req.RemoteAddr = loopbackPeer
		req.Header.Set("Authorization", "Bearer "+credential)
		res := httptest.NewRecorder()
		srv.http.Handler.ServeHTTP(res, req)
		if credential != token {
			assert.Equal(t, http.StatusNotFound, res.Code)
			assert.Empty(t, *revoked)
			assert.Equal(t, before, cfg.List()[0])

			continue
		}
		require.Equal(t, http.StatusOK, res.Code)
		assert.Equal(t, "no-store", res.Header().Get("Cache-Control"))
		var after cameraResponse
		require.NoError(t, json.Unmarshal(res.Body.Bytes(), &after))
		assert.NotEqual(t, before.SenderKey, after.SenderKey)
		assert.Equal(t, before.ReceiverKey, after.ReceiverKey)
		assert.Equal(t, srv.cameraResponse(after.Ingest).Links, after.Links)
		assert.Equal(t, []string{before.ID}, *revoked)
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/control/ingests/missing/reset-key", nil)
	req.RemoteAddr = loopbackPeer
	req.Header.Set("Authorization", "Bearer "+token)
	res := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(res, req)
	assert.Equal(t, http.StatusNotFound, res.Code)
	assert.Equal(t, []string{before.ID}, *revoked)
}
