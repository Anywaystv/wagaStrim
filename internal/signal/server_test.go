// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package signal

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// keyFrom is what lets one camera be reached by an encoder that takes a single
// URL and by one with a separate token field, so both spellings are covered
// here rather than only the one the settings page happens to print.
func TestKeyFrom(t *testing.T) {
	t.Parallel()

	const key = "s_0123456789abcdef0123456789abcdef"

	cases := []struct {
		name   string
		path   string
		header string
		want   string
	}{
		{name: "path carries it", path: key, want: key},
		{name: "bearer carries it", header: "Bearer " + key, want: key},
		{name: "bearer scheme is case insensitive", header: "bearer " + key, want: key},
		{name: "bearer tolerates surrounding space", header: "  Bearer   " + key + " ", want: key},
		// The path is the half a person can see, so a stale token in an
		// encoder's other field must not redirect the publish.
		{name: "path wins over a disagreeing bearer", path: key, header: "Bearer r_dead", want: key},
		{name: "no credential at all", want: ""},
		{name: "another scheme is not a key", header: "Basic " + key, want: ""},
		{name: "bearer with nothing after it", header: "Bearer ", want: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/whip", nil)
			if tc.path != "" {
				req.SetPathValue("key", tc.path)
			}

			if tc.header != "" {
				req.Header.Set("authorization", tc.header)
			}

			assert.Equal(t, tc.want, keyFrom(req))
		})
	}
}
