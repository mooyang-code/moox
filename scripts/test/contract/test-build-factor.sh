#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
tmp="$(mktemp -d "${TMPDIR:-/tmp}/moox-factor-build.XXXXXX")"
trap 'rm -rf "${tmp}"' EXIT
mkdir -p "${tmp}/scripts/build" "${tmp}/scripts/ci" "${tmp}/modules/factor" "${tmp}/tools"
cp "${ROOT}/scripts/build/build.sh" "${tmp}/scripts/build/build.sh"
cp "${ROOT}/scripts/ci/check-go-version.sh" "${tmp}/scripts/ci/check-go-version.sh"
cp "${ROOT}/.go-version" "${ROOT}/go.work" "${tmp}/"
cat >"${tmp}/tools/go" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
if [[ "$*" == 'env GOVERSION' ]]; then
  printf 'go%s\n' "${MOOX_TEST_GO_VERSION}"
  exit 0
fi
printf '%s|%s|%s|%s\n' "${GOOS}" "${GOARCH}" "${CGO_ENABLED}" "$*" >>"${BUILD_RECORD}"
MOCK
chmod +x "${tmp}/tools/go"
export PATH="${tmp}/tools:${PATH}"
export TARGET_GOOS=linux TARGET_GOARCH=amd64 BUILD_RECORD="${tmp}/record"
export MOOX_TEST_GO_VERSION="$(tr -d '[:space:]' < "${ROOT}/.go-version")"

bash "${tmp}/scripts/build/build.sh" factor-mgr
test "$(wc -l <"${BUILD_RECORD}" | tr -d ' ')" = 2
grep -Eq '^linux\|amd64\|0\|build .*moox-factor-mgr ./cmd/mgr$' "${BUILD_RECORD}"
grep -Eq '^linux\|amd64\|0\|build .*moox-factor-mgr-cli ./cmd/cli$' "${BUILD_RECORD}"
# Factor deployment must use the local build path for cross compilation too.
factor_build_block="$(sed -n '/if \[\[ "${WITH_FACTOR_MGR}" -eq 1 \]\]; then/,/if \[\[ "${WITH_STRATEGY}" -eq 1 \]\]; then/p' "${ROOT}/scripts/deploy/deploy-moox.sh")"
grep -Fq '"${ROOT}/scripts/build/build.sh" factor-mgr' <<<"${factor_build_block}"
! grep -Eq 'build-storage-linux|compile host|CGO-enabled' <<<"${factor_build_block}"
# Reject a pure Go target before inspecting any manifest or opening SSH.
if MOOX_LINUX_CGO_TARGET=factor-mgr CONFIG=/nonexistent/moox.toml bash "${ROOT}/scripts/build/build-storage-linux.sh" >"${tmp}/reject.log" 2>&1; then
  echo 'remote CGO builder accepted Factor' >&2
  exit 1
fi
grep -Fq 'unsupported linux CGO build target: factor-mgr' "${tmp}/reject.log"
echo "factor local build routing contract passed"
