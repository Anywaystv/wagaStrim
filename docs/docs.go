// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

// Package docs embeds the API guide so installed builds work offline.
package docs

import _ "embed"

//go:embed API.html
var API []byte
