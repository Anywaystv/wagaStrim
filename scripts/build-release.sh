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
python3 scripts/license-notices.py > "$staging/THIRD_PARTY_NOTICES.txt"
go build -trimpath -tags notray -o "$staging/$binary" ./cmd/wagastrim
cp LICENSES/MIT.txt "$staging/LICENSE.txt"
cp README.md "$staging/README.md"
COPYFILE_DISABLE=1 tar -czf "dist/wagastrim-$platform.tar.gz" -C "$staging" .
python3 - "dist/wagastrim-$platform.tar.gz" "$binary" <<'PY'
import os, sys, tarfile
from pathlib import Path
with tarfile.open(sys.argv[1]) as archive:
    expected = {sys.argv[2], 'README.md', 'THIRD_PARTY_NOTICES.txt', 'LICENSE.txt'}
    members = {member.name.removeprefix('./'): member for member in archive.getmembers() if member.isfile()}
    if set(members) != expected or any(member.size == 0 for member in members.values()):
        sys.exit('Release rejected: missing or unexpected archive contents')
    prefixes = [str(Path.home()), str(Path.cwd()), os.environ.get('GOMODCACHE', '')]
    for member in members.values():
        data = archive.extractfile(member).read()
        if any(prefix and prefix.encode() in data for prefix in prefixes):
            sys.exit('Release rejected: local build path in ' + member.name)
PY
echo "Built dist/wagastrim-$platform.tar.gz"
