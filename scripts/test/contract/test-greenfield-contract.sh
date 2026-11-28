#!/usr/bin/env bash
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

failures=0
check_absent() {
  local label=$1
  shift
  local matches
  if matches=$(rg -n "$@" 2>/dev/null); then
    printf '%s\n%s\n' "$label" "$matches" >&2
    failures=1
  fi
}

check_absent "reserved protocol declarations and markers are forbidden" \
  '\b[Rr]eserved\b' \
  --glob '*.proto' --glob '*.thrift' .
check_absent "deprecated protocol declarations and markers are forbidden" \
  '\b[Dd]eprecated\b' \
  --glob '*.proto' --glob '*.thrift' .

# Do not gate the greenfield contract on x/tools/deadcode. MooX exposes
# package-internal APIs through tRPC registration and retains bounded manual
# catch-up/E2E helpers alongside the Timer runtime; deadcode's Go 1.25 roots
# cannot see those reflective/test entry points and reports them as unreachable.
# Module-local go test/go vet and the explicit contract checks above remain the
# authoritative gates for this repository.

exit "$failures"
