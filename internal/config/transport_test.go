// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package config

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLANControlPersistsAndRollsBackFailedSave(t *testing.T) {
	cfg := &Config{path: t.TempDir() + "/config.json"}
	require.True(t, cfg.LANControlEnabled())
	require.NoError(t, cfg.SetLANControl(false))
	require.NoError(t, cfg.SetControlAccess("remote", true))
	raw, err := os.ReadFile(cfg.path)
	require.NoError(t, err)
	var saved Config
	require.NoError(t, json.Unmarshal(raw, &saved))
	require.False(t, saved.LANControlEnabled())
	require.True(t, saved.RemoteControlEnabled())
	cfg.path += "/invalid"
	require.Error(t, cfg.SetLANControl(true))
	require.False(t, cfg.LANControlEnabled())
	require.Error(t, cfg.SetControlAccess("remote", false))
	require.True(t, cfg.RemoteControlEnabled())
}

func TestOptionalHTTPSLinks(t *testing.T) {
	cfg := &Config{SignalPort: 7331}
	sender, receiver := cfg.SignalLinks("192.168.1.2")
	require.Equal(t, "whip://192.168.1.2:7331/whip/", sender)
	require.Equal(t, "http://192.168.1.2:7331/player/", receiver)
	cfg.TLSCert, cfg.TLSKey = "cert.pem", "key.pem"
	require.NoError(t, cfg.ValidateTransport())
	sender, receiver = cfg.SignalLinks("stream.example.com")
	require.Equal(t, "whips://stream.example.com:7331/whip/", sender)
	require.Equal(t, "https://stream.example.com:7331/player/", receiver)
	cfg.PublicURL = "https://stream.example.com"
	sender, receiver = cfg.SignalLinks("127.0.0.1")
	require.Equal(t, "whips://stream.example.com/whip/", sender)
	require.Equal(t, "https://stream.example.com/player/", receiver)
}

func TestRemoteControlDefaultsAndExplicitOptOut(t *testing.T) {
	for _, tc := range []struct {
		body string
		on   bool
	}{
		{`{}`, false},
		{`{"controlToken":"hosted-token"}`, true},
		{`{"controlToken":"hosted-token","controlRemote":false}`, false},
		{`{"controlToken":"hosted-token","controlRemote":true}`, true},
	} {
		var cfg Config
		require.NoError(t, json.Unmarshal([]byte(tc.body), &cfg))
		require.Equal(t, tc.on, cfg.RemoteControlEnabled())
	}
	cfg := &Config{path: t.TempDir() + "/config.json", ControlToken: "hosted-token"}
	require.NoError(t, cfg.SetControlAccess("remote", false))
	raw, err := os.ReadFile(cfg.path)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"controlRemote": false`)
	var saved Config
	require.NoError(t, json.Unmarshal(raw, &saved))
	require.False(t, saved.RemoteControlEnabled())
}

func TestTransportRejectsIncompleteTLSAndUnsafePublicURL(t *testing.T) {
	require.Error(t, (&Config{TLSCert: "cert.pem"}).ValidateTransport())
	for _, origin := range []string{
		"http://example.com", "https://user:pass@example.com",
		"https://example.com/path", "https://example.com?key=secret",
	} {
		require.Error(t, (&Config{PublicURL: origin}).ValidateTransport())
	}
}
