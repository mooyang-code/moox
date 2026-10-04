#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"

mapfile_compat() {
  local line
  while IFS= read -r line; do
    [[ -n "${line}" ]] && printf '%s\0' "${line}"
  done
}

while IFS= read -r -d '' module; do
  module="${module#./}"
  echo "==> go test ${module}"
  test_flags=(-count=1)
  case "${module}" in
    modules/admin|modules/hostagent|modules/trade)
      # goom relies on disabled inlining for its method interception tests.
      test_flags+=(-gcflags=all=-l -ldflags=-s=false)
      ;;
  esac
  (cd "${ROOT}/${module}" && go test "${test_flags[@]}" ./...)
  echo "==> go vet ${module}"
  (cd "${ROOT}/${module}" && go vet ./...)
done < <(
  awk '
    /^use \($/ { in_use=1; next }
    in_use && /^\)$/ { exit }
    in_use { gsub(/^[[:space:]]+|[[:space:]]+$/, ""); if ($0 != "") print }
  ' "${ROOT}/go.work" | mapfile_compat
)
