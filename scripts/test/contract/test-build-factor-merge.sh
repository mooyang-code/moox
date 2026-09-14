#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
tmp="$(mktemp -d "${TMPDIR:-/tmp}/moox-merge-build.XXXXXX")"
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

bash "${tmp}/scripts/build/build.sh" factor-merge
test "$(wc -l <"${BUILD_RECORD}" | tr -d ' ')" = 1
grep -Eq '^linux\|amd64\|1\|build .*moox-factor-merge ./cmd/merge$' "${BUILD_RECORD}"
echo "factor merge build routing contract passed"
