// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package signal

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Anywaystv/wagaStrim/internal/config"
	"github.com/Anywaystv/wagaStrim/internal/dynamicdelay"
	"github.com/Anywaystv/wagaStrim/internal/egress"
	"github.com/Anywaystv/wagaStrim/internal/relay"
	"github.com/pion/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlaybackRequiresReceiverKey(t *testing.T) {
	const receiverKey = "r_receiver"

	cfg := &config.Config{Ingests: []config.Ingest{{
		ID: "camera", SenderKey: "s_sender", ReceiverKey: receiverKey, DelayMS: 1000,
		Options: dynamicdelay.Options{Enabled: true, CatchUpMSPerSecond: 100},
	}}}
	log := logging.NewDefaultLoggerFactory().NewLogger("test")
	whep := egress.NewServer(cfg, log, nil, relay.New())
	srv := New(cfg, log, nil, whep)
	paths := []string{"playback", "dynamic/player.js"}
	for idx, key := range []string{"unknown", "s_sender", receiverKey} {
		for _, suffix := range paths {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/player/"+key+"/"+suffix, nil)
			req.RemoteAddr = fmt.Sprintf("192.0.2.%d:1234", idx+1)
			res := httptest.NewRecorder()
			srv.http.Handler.ServeHTTP(res, req)
			if key != receiverKey {
				assert.Equal(t, http.StatusNotFound, res.Code)
				assert.Equal(t, "not found\n", res.Body.String())

				continue
			}
			assert.Equal(t, http.StatusOK, res.Code)
			assert.Equal(t, "no-store", res.Header().Get("cache-control"))
			assert.NotContains(t, res.Body.String(), "s_sender")
			assert.NotContains(t, res.Body.String(), receiverKey)
			if suffix == "playback" {
				var playback dynamicdelay.Playback
				require.NoError(t, json.Unmarshal(res.Body.Bytes(), &playback))
				assert.True(t, playback.Enabled)
				assert.Equal(t, 100, playback.CatchUpMSPerSecond)
				assert.True(t, playback.Automatic())
				assert.Equal(t, 1000, playback.DelayMS)
			} else {
				assert.True(t, strings.HasPrefix(res.Header().Get("content-type"), "text/javascript"))
				assert.Equal(t, "nosniff", res.Header().Get("x-content-type-options"))
			}
		}
	}
	for _, asset := range []string{"controls.html", "controls.js", "unknown.js"} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/player/r_receiver/dynamic/"+asset, nil)
		res := httptest.NewRecorder()
		srv.http.Handler.ServeHTTP(res, req)
		assert.Equal(t, http.StatusNotFound, res.Code)
	}
	cfg.Ingests[0].SyncGroup = "rig"
	assert.False(t, whep.Playback("camera").Enabled)
}
