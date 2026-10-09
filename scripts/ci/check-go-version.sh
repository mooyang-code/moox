#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
required="$(tr -d '[:space:]' < "${ROOT}/.go-version")"
actual="$(GOTOOLCHAIN=local go env GOVERSION)"
if [[ "${actual}" != "go${required}" ]]; then
  echo "MooX requires preinstalled Go ${required}; found ${actual}. Install the pinned toolchain before building." >&2
  exit 1
fi
workspace="$(awk '$1 == "go" {print $2; exit}' "${ROOT}/go.work")"
[[ "${workspace}" == "${required}" ]] || { echo '.go-version and go.work disagree' >&2; exit 1; }
echo "Go toolchain: ${actual} (preinstalled)"
