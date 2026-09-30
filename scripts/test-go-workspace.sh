#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COVERAGE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/moox-go-coverage.XXXXXX")"
trap 'rm -rf "${COVERAGE_DIR}"' EXIT
rm -f "${ROOT}/coverage.out"
touch "${COVERAGE_DIR}/modules.tsv"
module_index=0

mapfile_compat() {
  local line
  while IFS= read -r line; do
    [[ -n "${line}" ]] && printf '%s\0' "${line}"
  done
}

while IFS= read -r -d '' module; do
  module="${module#./}"
  echo "==> go test ${module}"
  coverage_profile="${COVERAGE_DIR}/${module_index}.out"
  test_flags=(-count=1 -vet=off -covermode=set "-coverprofile=${coverage_profile}")
  case "${module}" in
    modules/admin|modules/hostagent|modules/trade)
      # goom relies on disabled inlining for its method interception tests.
      test_flags+=(-gcflags=all=-l -ldflags=-s=false)
      ;;
  esac
  (cd "${ROOT}/${module}" && go test "${test_flags[@]}" ./...)
  printf '%s\t%s\n' "${module}" "${coverage_profile}" >> "${COVERAGE_DIR}/modules.tsv"
  module_index=$((module_index + 1))
done < <(
  awk '
    /^use \($/ { in_use=1; next }
    in_use && /^\)$/ { exit }
    in_use { gsub(/^[[:space:]]+|[[:space:]]+$/, ""); if ($0 != "") print }
  ' "${ROOT}/go.work" | mapfile_compat
)

python3 "${ROOT}/scripts/check-go-coverage.py" "${ROOT}" "${COVERAGE_DIR}/modules.tsv" "${ROOT}/coverage.out"
