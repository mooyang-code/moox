#!/usr/bin/env bash
# moox-factor-mgr and moox-factor-engine share modules/factor; this check keeps
# the engine free of the manager's SQLite catalog and the manager free of the
# event-driven compute runtime.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PREFIX="github.com/mooyang-code/moox/modules/factor/internal/"
cd "${ROOT}/modules/factor"

violations=()
check() {
  local binary="$1"
  shift
  local deps
  deps="$(go list -deps "./cmd/${binary}")"
  for forbidden in "$@"; do
    if grep -qx "${PREFIX}${forbidden}" <<<"${deps}"; then
      violations+=("cmd/${binary} must not depend on internal/${forbidden}")
    fi
  done
}

check engine store catalog rpc enginehub recalc
check mgr engine trigger/eventconsumer recalcexec

if grep -qx "modernc.org/sqlite" <<<"$(go list -deps ./cmd/engine)"; then
  violations+=("cmd/engine must not link SQLite")
fi

if ((${#violations[@]} > 0)); then
  printf 'factor split boundary violation: %s\n' "${violations[@]}" >&2
  exit 1
fi
echo "factor split boundaries ok"
