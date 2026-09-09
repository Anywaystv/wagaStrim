#!/bin/sh
# SPDX-FileCopyrightText: 2026 wagaStrim contributors
# SPDX-License-Identifier: MIT
set -eu

cd "$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
platform="$(go env GOOS)-$(go env GOARCH)"
mkdir -p dist
staging=$(mktemp -d)
trap 'rm -rf "$staging"' EXIT
binary=wagastrim
if [ "$(go env GOOS)" = windows ]; then binary=wagastrim.exe; fi
go mod download all
# Run tooling on the host even when the executable targets another platform.
GOOS="$(go env GOHOSTOS)" GOARCH="$(go env GOHOSTARCH)" go build -o "$staging/tools" scripts/tools.go
"$staging/tools" notices > "$staging/THIRD_PARTY_NOTICES.txt"
go build -trimpath -tags notray -o "$staging/$binary" ./cmd/wagastrim
cp LICENSES/MIT.txt "$staging/LICENSE.txt"
cp README.md "$staging/README.md"
COPYFILE_DISABLE=1 tar -czf "dist/wagastrim-$platform.tar.gz" -C "$staging" "$binary" LICENSE.txt README.md THIRD_PARTY_NOTICES.txt
"$staging/tools" verify "dist/wagastrim-$platform.tar.gz" "$binary"
echo "Built dist/wagastrim-$platform.tar.gz"
