#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

unformatted="$(git ls-files -z -- '*.go' ':!web-host/internal/statik/statik.go' | xargs -0 gofmt -l)"
if [[ -n "${unformatted}" ]]; then
  {
    echo 'Go files are not gofmt formatted:'
    printf '%s\n' "${unformatted}"
    echo
    echo 'Run gofmt on the listed files before committing.'
  } >&2
  exit 1
fi

echo '==> gofmt check passed'
