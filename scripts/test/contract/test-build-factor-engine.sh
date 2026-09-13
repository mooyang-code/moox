#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
tmp="$(mktemp -d "${TMPDIR:-/tmp}/moox-engine-build.XXXXXX")"
trap 'rm -rf "${tmp}"' EXIT
mkdir -p "${tmp}/scripts/build" "${tmp}/modules/factor" "${tmp}/tools"
cp "${ROOT}/scripts/build/build.sh" "${tmp}/scripts/build/build.sh"
cat >"${tmp}/tools/go" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
printf '%s|%s|%s|%s\n' "${GOOS}" "${GOARCH}" "${CGO_ENABLED}" "$*" >>"${BUILD_RECORD}"
MOCK
chmod +x "${tmp}/tools/go"
export PATH="${tmp}/tools:${PATH}"
export TARGET_GOOS=linux TARGET_GOARCH=amd64 BUILD_RECORD="${tmp}/record"

bash "${tmp}/scripts/build/build.sh" factor-engine
test "$(wc -l <"${BUILD_RECORD}" | tr -d ' ')" = 1
grep -Eq '^linux\|amd64\|1\|build .*moox-factor-engine ./cmd/engine$' "${BUILD_RECORD}"

rm "${BUILD_RECORD}"
bash "${tmp}/scripts/build/build.sh" factor
test "$(wc -l <"${BUILD_RECORD}" | tr -d ' ')" = 3
grep -Eq '^linux\|amd64\|1\|build .*moox-factor ./cmd/server$' "${BUILD_RECORD}"
grep -Eq '^linux\|amd64\|1\|build .*moox-factor-cli ./cmd/cli$' "${BUILD_RECORD}"
grep -Eq '^linux\|amd64\|1\|build .*moox-factor-engine ./cmd/engine$' "${BUILD_RECORD}"
echo "factor engine build routing contract passed"
