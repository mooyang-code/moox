#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

mapfile_compat() {
  local line
  while IFS= read -r line; do
    [[ -n "${line}" ]] && printf '%s\0' "${line}"
  done
}

while IFS= read -r -d '' module; do
  module="${module#./}"
  echo "==> go test ${module}"
  (cd "${ROOT}/${module}" && go test -count=1 ./...)
  echo "==> go vet ${module}"
  (cd "${ROOT}/${module}" && go vet ./...)
done < <(
  awk '
    /^use \($/ { in_use=1; next }
    in_use && /^\)$/ { exit }
    in_use { gsub(/^[[:space:]]+|[[:space:]]+$/, ""); if ($0 != "") print }
  ' "${ROOT}/go.work" | mapfile_compat
)
