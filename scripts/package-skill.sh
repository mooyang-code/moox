#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="${ROOT}/dist/moox-skill.tar.gz"

mkdir -p "${ROOT}/dist"
if [[ ! -d "${ROOT}/skills/moox" ]]; then
  echo "missing skill directory: ${ROOT}/skills/moox" >&2
  exit 1
fi

tar -C "${ROOT}/skills" -czf "${OUT}" moox
echo "==> skill package: ${OUT}"
