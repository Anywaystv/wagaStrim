// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package config

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// The settings page mutates the ingest list while the public signaling listener
// resolves keys against it. Those are different goroutines on different
// listeners, so the config has to be safe for concurrent use.
func TestConcurrentAddAndResolve(t *testing.T) {
	cfg := &Config{path: t.TempDir() + "/config.json"}

	ing, err := cfg.AddIngest("first")
	require.NoError(t, err)

	key := ing.SenderKey

	var wg sync.WaitGroup

	wg.Add(2)

	go func() {
		defer wg.Done()

		for range 50 {
			_, _ = cfg.AddIngest("more")
		}
	}()

	go func() {
		defer wg.Done()

		for range 50 {
			cfg.Resolve(key)
		}
	}()

	wg.Wait()
}
