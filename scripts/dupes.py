#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 wagaStrim contributors
# SPDX-License-Identifier: MIT
"""Report functions with identical bodies anywhere in the tree.

golangci-lint's dupl cannot see these. It analyses one package at a time for one
GOOS, so a function copied into another package is invisible to it, and so is a
copy in a file excluded by a build tag. Both have happened in this repository:
the SDP exchange was byte-identical in ingest and egress, and run was
byte-identical in the darwin and linux autostart files.
"""
import hashlib
import pathlib
import re
import sys

MIN_BODY_CHARS = 100

# Comments differ between copies far more often than code does, and a copy with
# a reworded comment is still a copy.
COMMENT = re.compile(r"//.*?$|/\*.*?\*/", re.S | re.M)


def functions(path):
    """Yield (name, normalised body) for each top-level func in a file."""
    text = path.read_text()
    for match in re.finditer(r"^func\s+(?:\([^)]*\)\s*)?(\w+)", text, re.M):
        start = text.find("{", match.end())
        if start < 0:
            continue

        depth, index = 0, start
        while index < len(text):
            if text[index] == "{":
                depth += 1
            elif text[index] == "}":
                depth -= 1
                if depth == 0:
                    break
            index += 1

        body = COMMENT.sub("", text[start : index + 1])
        body = " ".join(body.split())
        if len(body) >= MIN_BODY_CHARS:
            yield match.group(1), body


def main():
    root = pathlib.Path(__file__).resolve().parent.parent
    seen = {}
    clashes = []

    for path in sorted(root.rglob("*.go")):
        if ".git" in path.parts or path.name.endswith("_test.go"):
            continue

        for name, body in functions(path):
            key = hashlib.sha256(body.encode()).hexdigest()
            where = f"{path.relative_to(root)}:{name}"
            if key in seen:
                clashes.append((seen[key], where))
            else:
                seen[key] = where

    if not clashes:
        print("no duplicated function bodies")
        return 0

    for first, second in clashes:
        print(f"  {first}\n  {second}\n")

    print("duplicated function bodies above. Share them, or say why not.")
    return 1


if __name__ == "__main__":
    sys.exit(main())
