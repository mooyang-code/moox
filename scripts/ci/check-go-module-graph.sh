#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT}"
bash scripts/ci/check-go-version.sh
graph="$(mktemp "${TMPDIR:-/tmp}/moox-module-graph.XXXXXX")"
trap 'rm -f "${graph}"' EXIT
# Do not use -e: missing dependency metadata must fail this gate.
GOTOOLCHAIN=local go list -m -json all > "${graph}"
python3 - "${graph}" .go-version <<'PY'
import json
import pathlib
import re
import sys


def version(value):
    if not re.fullmatch(r"\d+\.\d+(?:\.\d+)?", value):
        raise SystemExit(f"Unsupported Go version in module graph: {value}")
    return tuple(map(int, value.split("."))) + (0,) * (3 - len(value.split(".")))


pinned = pathlib.Path(sys.argv[2]).read_text().strip()
raw = pathlib.Path(sys.argv[1]).read_text()
decoder = json.JSONDecoder()
modules = []
while raw.strip():
    entry, end = decoder.raw_decode(raw.lstrip())
    raw = raw.lstrip()[end:]
    if entry.get("Error"):
        raise SystemExit(f"Unresolved module: {entry['Path']}")
    selected = entry.get("Replace", entry)
    minimum = selected.get("GoVersion", "1.0")
    if version(minimum) > version(pinned):
        raise SystemExit(f"{entry['Path']} requires Go {minimum}, above pinned {pinned}")
    modules.append((entry, minimum))
if not modules:
    raise SystemExit("Empty module graph")
external = [item for item in modules if not item[0].get("Main")]
highest = max(external, key=lambda item: version(item[1]))
print(f"Module graph: {len(modules)} modules; highest dependency minimum Go {highest[1]} ({highest[0]['Path']}); pinned Go {pinned}")
PY
