#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 wagaStrim contributors
# SPDX-License-Identifier: MIT
"""Print dependency license texts from the locked source dependencies."""
import json
import os
from pathlib import Path
import subprocess
import sys

root = Path(__file__).resolve().parent.parent
os.chdir(root)
if (root / "Cargo.toml").exists():
    metadata = json.loads(subprocess.check_output([
        "cargo", "metadata", "--locked", "--format-version", "1",
        "--filter-platform", "aarch64-apple-ios", "--no-default-features",
        "--features", "apple-crypto",
    ]))
    packages = [
        (p["name"], p["version"], Path(p["manifest_path"]).parent, p.get("license_file"))
        for p in metadata["packages"] if p["id"] not in metadata["workspace_members"]
    ]
else:
    output = subprocess.check_output(["go", "list", "-m", "-json", "all"], text=True)
    decoder = json.JSONDecoder()
    packages = []
    while output.strip():
        package, end = decoder.raw_decode(output.lstrip())
        output = output.lstrip()[end:]
        if not package.get("Main"):
            source = package.get("Replace", package)
            if "Dir" not in source:
                sys.exit("Missing dependency source: " + package["Path"] + "; run go mod download")
            packages.append((package["Path"], package["Version"], Path(source["Dir"]), None))

sections = ["THIRD-PARTY NOTICES\n\nIncludes dependency and build-tool notices. "
            "Original license terms and copyright notices follow.\n"]
for name, version, directory, declared in sorted(packages):
    files = []
    for folder, dirs, names in os.walk(directory):
        dirs[:] = sorted(d for d in dirs if d not in {".git", "target", ".build"})
        for filename in sorted(names):
            upper = filename.upper()
            in_license_dir = any(part.upper() in {"LICENSES", "LICENCES"}
                                 for part in Path(folder).relative_to(directory).parts)
            if in_license_dir or upper.startswith(("LICENSE", "LICENCE", "NOTICE", "COPYING", "COPYRIGHT")):
                files.append(Path(folder) / filename)
    if declared:
        files.append(directory / declared)
    files = sorted(set(files))
    if not files:
        sys.exit("No license text found for " + name + " " + version)
    sections.append("\n=== " + name + " " + version + " ===\n")
    for path in files:
        sections.append("\n--- " + str(path.relative_to(directory)) + " ---\n")
        sections.append(path.read_text(encoding="utf-8") + "\n")
sys.stdout.write("".join(sections))
