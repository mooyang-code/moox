#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
required="$(tr -d '[:space:]' < "${ROOT}/.go-version")"
actual="$(GOTOOLCHAIN=local go env GOVERSION)"
if [[ "${actual}" != "go${required}" ]]; then
  echo "MooX requires preinstalled Go ${required}; found ${actual}. Install the pinned toolchain before building." >&2
  exit 1
fi
gomod="$(awk '$1 == "go" {print $2; exit}' "${ROOT}/go.mod")"
[[ "${gomod}" == "${required}" ]] || { echo '.go-version and go.mod disagree' >&2; exit 1; }
echo "Go toolchain: ${actual} (preinstalled)"
