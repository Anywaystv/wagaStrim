// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package listen

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/stretchr/testify/require"
)

type addressLogger struct {
	logging.LeveledLogger
	address chan string
}

func (l addressLogger) Infof(_ string, args ...any) { l.address <- fmt.Sprint(args[1]) }

func TestServeTLSUsesConfiguredCertificate(t *testing.T) {
	fixture := httptest.NewTLSServer(http.NotFoundHandler())
	defer fixture.Close()
	certificate := fixture.TLS.Certificates[0]
	key, err := x509.MarshalPKCS8PrivateKey(certificate.PrivateKey)
	require.NoError(t, err)
	dir := t.TempDir()
	certFile, keyFile := dir+"/cert.pem", dir+"/key.pem"
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Certificate[0]})
	require.NoError(t, os.WriteFile(certFile, certPEM, 0o600))
	require.NoError(t, os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0o600))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	log := addressLogger{logging.NewDefaultLoggerFactory().NewLogger("test"), make(chan string, 1)}
	srv := &http.Server{Handler: http.NotFoundHandler(), ReadHeaderTimeout: time.Second}
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, log, srv, "127.0.0.1:0", "test", certFile, keyFile) }()
	var address string
	select {
	case address = <-log.address:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "listener did not start")
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+address, nil)
	require.NoError(t, err)
	client := fixture.Client()
	client.Timeout = 5 * time.Second
	response, err := client.Do(request)
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.NotNil(t, response.TLS)
	require.Positive(t, srv.ReadTimeout)
	require.Positive(t, srv.WriteTimeout)
	require.Positive(t, srv.IdleTimeout)
	cancel()
	require.NoError(t, <-done)
}
