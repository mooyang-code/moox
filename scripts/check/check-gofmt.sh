#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT}"

go_files=()
while IFS= read -r -d '' file; do
  if [[ -f "${file}" ]]; then
    go_files+=("${file}")
  fi
done < <(git ls-files -z -- '*.go' ':!web-host/internal/statik/statik.go')

unformatted="$(printf '%s\0' "${go_files[@]}" | xargs -0 -r gofmt -l)"
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
