#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
HELPER="${ROOT}/scripts/lib/caddy-managed.sh"
TMP=$(mktemp -d "${TMPDIR:-/tmp}/moox-caddy-managed.XXXXXX")
cleanup() { "${HELPER}" stop --deploy-dir "${TMP}/deploy" >/dev/null 2>&1 || true; rm -rf "${TMP}"; }
trap cleanup EXIT

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
assert_contains() { grep -Fq "$2" "$1" || fail "$1 does not contain $2"; }

[[ -x "${HELPER}" ]] || fail "managed Caddy helper is missing"
assert_contains "${ROOT}/scripts/deps/caddy-v2.11.4-checksums.txt" 'caddy_2.11.4_linux_amd64.tar.gz'
assert_contains "${ROOT}/scripts/deploy-moox.sh" 'caddy-managed.sh'
assert_contains "${ROOT}/scripts/release.sh" 'caddy-managed.sh'
assert_contains "${ROOT}/scripts/deploy-moox.sh" 'MOOX_WEB_HOST_ADDR:-127.0.0.1:9528'
assert_contains "${ROOT}/scripts/deploy-moox.sh" 'verify_public_https'
assert_contains "${ROOT}/scripts/deploy-moox.sh" 'public HTTPS acceptance failed'
assert_contains "${ROOT}/scripts/deploy-moox.sh" 'Caddyfile.rollback'
assert_contains "${ROOT}/scripts/deploy-moox.sh" 'moox-caddy-data.XXXXXX'
assert_contains "${ROOT}/scripts/deploy-moox.sh" 'mv "${DEPLOY_DIR}/data/caddy" "${CADDY_DATA_TMP}/caddy"'
assert_contains "${HELPER}" 'rollback)'
assert_contains "${HELPER}" 'command -v ss'
assert_contains "${HELPER}" '/proc/net/tcp'

mkdir -p "${TMP}/fixture" "${TMP}/deploy/config/caddy"
cat >"${TMP}/fixture/caddy" <<'SH'
#!/usr/bin/env bash
case "${1:-}" in
  version) echo 'v2.11.4 h1:test' ;;
  validate) exit "${FAKE_VALIDATE_EXIT:-0}" ;;
  reload) printf 'reload\n' >>"${FAKE_LOG}" ;;
  run) printf 'run\n' >>"${FAKE_LOG}"; exec python3 -m http.server "${FAKE_ADMIN_PORT}" --bind 127.0.0.1 ;;
esac
SH
chmod +x "${TMP}/fixture/caddy"
tar -C "${TMP}/fixture" -czf "${TMP}/caddy_2.11.4_linux_amd64.tar.gz" caddy
digest=$(shasum -a 512 "${TMP}/caddy_2.11.4_linux_amd64.tar.gz" | awk '{print $1}')
printf '%s  %s\n' "${digest}" caddy_2.11.4_linux_amd64.tar.gz >"${TMP}/checksums.txt"
EDGE_PORT=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')
ADMIN_PORT=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')
printf ':%s { respond "ok" }\n' "${EDGE_PORT}" >"${TMP}/candidate.Caddyfile"

export FAKE_LOG="${TMP}/caddy.log"
export FAKE_ADMIN_PORT="${ADMIN_PORT}"
export MOOX_CADDY_ADMIN_ENDPOINT="127.0.0.1:${ADMIN_PORT}"
export MOOX_CADDY_ADMIN_PATH=/
export MOOX_CADDY_SKIP_PID_EXE_CHECK=1
export MOOX_CADDY_SKIP_CA_WAIT=1
MOOX_CADDY_ARCHIVE="${TMP}/caddy_2.11.4_linux_amd64.tar.gz" \
MOOX_CADDY_CHECKSUMS="${TMP}/checksums.txt" \
  "${HELPER}" install --deploy-dir "${TMP}/deploy" --os linux --arch amd64
[[ $("${TMP}/deploy/bin/caddy" version) == v2.11.4* ]] || fail 'exact managed version was not installed'

cp "${TMP}/candidate.Caddyfile" "${TMP}/deploy/config/caddy/Caddyfile"
"${HELPER}" check --deploy-dir "${TMP}/deploy" >/dev/null
"${HELPER}" start --deploy-dir "${TMP}/deploy"
sleep 0.1
pid=$(cat "${TMP}/deploy/run/caddy.pid")
kill -0 "${pid}" || fail 'managed Caddy did not start'
status_json=
for _ in $(seq 1 30); do
  status_json=$("${HELPER}" status --deploy-dir "${TMP}/deploy")
  grep -Fq '"admin_healthy":true' <<<"${status_json}" && break
  sleep 0.1
done
grep -Fq '"admin_healthy":true' <<<"${status_json}" || fail 'managed admin endpoint was not verified'
"${HELPER}" check --deploy-dir "${TMP}/deploy"
if MOOX_CADDY_ADMIN_ENDPOINT=127.0.0.1:1 "${HELPER}" check --deploy-dir "${TMP}/deploy" >/dev/null 2>&1; then
  fail 'managed Caddy with an unverified admin endpoint was accepted'
fi
"${HELPER}" ensure --deploy-dir "${TMP}/deploy" --os linux --arch amd64
assert_contains "${FAKE_LOG}" reload
"${HELPER}" stop --deploy-dir "${TMP}/deploy"

printf '999999\n' >"${TMP}/deploy/run/caddy.pid"
"${HELPER}" stop --deploy-dir "${TMP}/deploy"
[[ ! -e "${TMP}/deploy/run/caddy.pid" ]] || fail 'stale PID file was not removed'

cp /bin/sh "${TMP}/deploy/bin/caddy"
if "${HELPER}" check --deploy-dir "${TMP}/deploy" >/dev/null 2>&1; then
  fail 'wrong managed version was accepted'
fi

printf 'PASS: pinned rootless Caddy install/start/reload/stale-pid contracts\n'
