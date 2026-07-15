#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
HELPER="${ROOT}/scripts/lib/caddy-managed.sh"
LOOPBACK_HELPER="${ROOT}/scripts/lib/loopback-listeners.sh"
TMP=$(mktemp -d "${TMPDIR:-/tmp}/moox-caddy-managed.XXXXXX")
cleanup() { "${HELPER}" stop --deploy-dir "${TMP}/deploy" >/dev/null 2>&1 || true; rm -rf "${TMP}"; }
trap cleanup EXIT

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
assert_contains() { grep -Fq -- "$2" "$1" || fail "$1 does not contain $2"; }

[[ -x "${HELPER}" ]] || fail "managed Caddy helper is missing"
assert_contains "${ROOT}/scripts/deps/caddy-v2.11.4-checksums.txt" 'caddy_2.11.4_linux_amd64.tar.gz'
assert_contains "${ROOT}/scripts/deploy-moox.sh" 'caddy-managed.sh'
assert_contains "${ROOT}/scripts/release.sh" 'caddy-managed.sh'
assert_contains "${ROOT}/scripts/deploy-moox.sh" 'MOOX_WEB_HOST_ADDR:-127.0.0.1:9528'
assert_contains "${ROOT}/scripts/deploy-moox.sh" 'validate_moox_loopback_listeners'
assert_contains "${LOOPBACK_HELPER}" 'MOOX_WEB_HOST_HEALTH_ADDR-127.0.0.1:19527'
assert_contains "${LOOPBACK_HELPER}" 'MOOX_ADMIN_CONTROL_ADDR-127.0.0.1:11000'
assert_contains "${LOOPBACK_HELPER}" 'MOOX_ADMIN_SERVICE_ADDR-127.0.0.1:11002'
assert_contains "${LOOPBACK_HELPER}" 'MOOX_GATEWAY_SERVICE_ADDR-127.0.0.1:11002'
assert_contains "${LOOPBACK_HELPER}" 'MOOX_GATEWAY_HEALTH_ADDR-127.0.0.1:11012'
assert_contains "${ROOT}/scripts/deploy-moox.sh" 'moox-archive" -config=config/app.yaml -conf=config/trpc_go.yaml'
assert_contains "${ROOT}/scripts/deploy-moox.sh" 'verify_public_https'
assert_contains "${ROOT}/scripts/deploy-moox.sh" 'public HTTPS acceptance failed'
assert_contains "${ROOT}/scripts/deploy-moox.sh" '--target-ca <auto|skip>'
assert_contains "${ROOT}/scripts/deploy-moox.sh" 'configure_target_ca'
assert_contains "${ROOT}/scripts/deploy-moox.sh" 'default_local_ca_output'
assert_contains "${ROOT}/scripts/deploy-moox.sh" 'local CA already trusted'
assert_contains "${ROOT}/scripts/deploy-moox.sh" 'local CA is not trusted; installing'
assert_contains "${ROOT}/scripts/deploy-moox.sh" 'MOOX_CA_SUDO_NONINTERACTIVE=1'
assert_contains "${ROOT}/scripts/deploy-moox.sh" 'skills/moox/scripts/caddy-ca.sh'
assert_contains "${ROOT}/scripts/deploy-moox.sh" 'local args=(install-target'
assert_contains "${ROOT}/scripts/deploy-moox.sh" '77)'
assert_contains "${ROOT}/scripts/deploy-moox.sh" 'target CA trust installation failed with status'
assert_contains "${ROOT}/scripts/deploy-moox.sh" 'cp "${ROOT}/scripts/install-caddy-ca.sh" "${STAGE_DIR}/lib/install-caddy-ca.sh"'
assert_contains "${ROOT}/scripts/deploy-moox.sh" 'cp "${ROOT}/scripts/lib/loopback-listeners.sh" "${STAGE_DIR}/lib/loopback-listeners.sh"'
assert_contains "${HELPER}" '${CONFIG}.rollback'
assert_contains "${ROOT}/scripts/deploy-moox.sh" 'Caddyfile.next'
assert_contains "${ROOT}/scripts/deploy-moox.sh" '--config "${deploy_dir}/config/caddy/Caddyfile.next"'
assert_contains "${ROOT}/scripts/deploy-moox.sh" 'moox-caddy-data.XXXXXX'
assert_contains "${ROOT}/scripts/deploy-moox.sh" 'mv "${DEPLOY_DIR}/data/caddy" "${CADDY_DATA_TMP}/caddy"'
assert_contains "${HELPER}" 'rollback)'
assert_contains "${HELPER}" 'command -v ss'
assert_contains "${HELPER}" '/proc/net/tcp'

[[ -x "${LOOPBACK_HELPER}" ]] || fail 'loopback listener validator is missing'
"${LOOPBACK_HELPER}"
MOOX_WEB_HOST_ADDR=127.0.0.1:08080 "${LOOPBACK_HELPER}"
for assignment in \
  'MOOX_WEB_HOST_ADDR=0.0.0.0:9528' \
  'MOOX_WEB_HOST_HEALTH_ADDR=localhost:19527' \
  'MOOX_ADMIN_CONTROL_ADDR=' \
  'MOOX_ADMIN_SERVICE_ADDR=127.0.0.1:0' \
  'MOOX_ADMIN_SERVICE_ADDR=127.0.0.1:65536' \
  'MOOX_ADMIN_SERVICE_ADDR=127.0.0.1:099999' \
  'MOOX_ADMIN_SERVICE_ADDR=127.0.0.1:99999999999999999999'; do
  if env "${assignment}" "${LOOPBACK_HELPER}" >/dev/null 2>&1; then
    fail "loopback validator accepted ${assignment}"
  fi
done

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
export FAKE_LOG="${TMP}/caddy.log"

mkdir -p "${TMP}/cap-bin"
cat >"${TMP}/cap-bin/getcap" <<'SH'
#!/usr/bin/env bash
if [[ -s "${FAKE_CAP_STATE}" ]]; then
  printf '%s cap_net_bind_service=ep\n' "$1"
fi
SH
cat >"${TMP}/cap-bin/sudo" <<'SH'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"${FAKE_SUDO_LOG}"
[[ "${FAKE_SUDO_FAIL:-0}" != 1 ]] || exit 1
[[ "$1" == -n && "$2" == setcap && "$3" == cap_net_bind_service=+ep ]] || exit 2
printf '%s\n' "$4" >"${FAKE_CAP_STATE}"
SH
chmod +x "${TMP}/cap-bin/getcap" "${TMP}/cap-bin/sudo"

CAP_DEPLOY="${TMP}/cap-deploy"
CAP_STATE="${TMP}/cap.state"
SUDO_LOG="${TMP}/sudo.log"
FAKE_CAP_STATE="${CAP_STATE}" FAKE_SUDO_LOG="${SUDO_LOG}" PATH="${TMP}/cap-bin:${PATH}" \
MOOX_CADDY_ARCHIVE="${TMP}/caddy_2.11.4_linux_amd64.tar.gz" MOOX_CADDY_CHECKSUMS="${TMP}/checksums.txt" \
  "${HELPER}" install --deploy-dir "${CAP_DEPLOY}" --os linux --arch amd64 --ports 443
grep -Fxq -- '-n setcap cap_net_bind_service=+ep '"${CAP_DEPLOY}/bin/caddy" "${SUDO_LOG}" || \
  fail 'privileged-port install did not grant only cap_net_bind_service'

# An existing version-correct binary must recover a missing capability, and a
# subsequent binary replacement must grant it again.
mkdir -p "${CAP_DEPLOY}/config/caddy"
printf 'MOOX_BROWSER_HTTPS_PORT=9527\nMOOX_SERVICE_HTTPS_PORT=443\n' >"${CAP_DEPLOY}/config/caddy/edge.env"
rm -f "${CAP_STATE}"; : >"${SUDO_LOG}"
FAKE_CAP_STATE="${CAP_STATE}" FAKE_SUDO_LOG="${SUDO_LOG}" PATH="${TMP}/cap-bin:${PATH}" \
  "${HELPER}" ensure --deploy-dir "${CAP_DEPLOY}" --os linux --arch amd64
[[ -s "${CAP_STATE}" ]] || fail 'existing managed Caddy did not recover its privileged-port capability'
rm -f "${CAP_STATE}"; : >"${SUDO_LOG}"
FAKE_CAP_STATE="${CAP_STATE}" FAKE_SUDO_LOG="${SUDO_LOG}" PATH="${TMP}/cap-bin:${PATH}" \
MOOX_CADDY_ARCHIVE="${TMP}/caddy_2.11.4_linux_amd64.tar.gz" MOOX_CADDY_CHECKSUMS="${TMP}/checksums.txt" \
  "${HELPER}" install --deploy-dir "${CAP_DEPLOY}" --os linux --arch amd64 --ports 443
[[ -s "${CAP_STATE}" ]] || fail 'managed Caddy replacement did not reapply its privileged-port capability'

: >"${SUDO_LOG}"
FAKE_CAP_STATE="${CAP_STATE}" FAKE_SUDO_LOG="${SUDO_LOG}" PATH="${TMP}/cap-bin:${PATH}" \
  "${HELPER}" ensure --deploy-dir "${CAP_DEPLOY}" --os linux --arch amd64 --ports 8443
[[ ! -s "${SUDO_LOG}" ]] || fail 'nonprivileged Caddy ports unexpectedly invoked sudo'

rm -f "${CAP_STATE}"; : >"${SUDO_LOG}"
printf ':443 { respond "ok" }\n' >"${CAP_DEPLOY}/config/caddy/Caddyfile"
: >"${FAKE_LOG}"
if FAKE_CAP_STATE="${CAP_STATE}" FAKE_SUDO_LOG="${SUDO_LOG}" FAKE_SUDO_FAIL=1 PATH="${TMP}/cap-bin:${PATH}" \
  "${HELPER}" ensure --deploy-dir "${CAP_DEPLOY}" --os linux --arch amd64 --ports 443 \
    >"${TMP}/cap-failure.out" 2>&1; then
  fail 'privileged-port ensure accepted a missing capability after setcap failed'
fi
grep -Fq 'cap_net_bind_service' "${TMP}/cap-failure.out" || fail 'privileged-port capability failure was unclear'
! grep -Fq 'run' "${FAKE_LOG}" || fail 'Caddy start was attempted after privileged-port capability setup failed'

EDGE_PORT=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')
ADMIN_PORT=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')
printf ':%s { respond "ok" }\n' "${EDGE_PORT}" >"${TMP}/candidate.Caddyfile"

export FAKE_ADMIN_PORT="${ADMIN_PORT}"
export MOOX_CADDY_ADMIN_ENDPOINT="127.0.0.1:${ADMIN_PORT}"
export MOOX_CADDY_ADMIN_PATH=/
export MOOX_CADDY_SKIP_PID_EXE_CHECK=1
export MOOX_CADDY_SKIP_CA_WAIT=1
MOOX_CADDY_ARCHIVE="${TMP}/caddy_2.11.4_linux_amd64.tar.gz" \
MOOX_CADDY_CHECKSUMS="${TMP}/checksums.txt" \
  "${HELPER}" install --deploy-dir "${TMP}/deploy" --os linux --arch amd64
[[ $("${TMP}/deploy/bin/caddy" version) == v2.11.4* ]] || fail 'exact managed version was not installed'

cp "${TMP}/candidate.Caddyfile" "${TMP}/first.Caddyfile"
MOOX_CADDY_ARCHIVE="${TMP}/caddy_2.11.4_linux_amd64.tar.gz" \
MOOX_CADDY_CHECKSUMS="${TMP}/checksums.txt" \
  "${HELPER}" ensure --deploy-dir "${TMP}/first-deploy" --os linux --arch amd64 --config "${TMP}/first.Caddyfile" \
  >/dev/null 2>&1 || fail 'first candidate did not start'
"${HELPER}" rollback --deploy-dir "${TMP}/first-deploy"
[[ ! -e "${TMP}/first-deploy/config/caddy/Caddyfile" ]] || fail 'failed first-install config remained active after rollback'

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
MOOX_PUBLIC_HOST=127.0.0.1 MOOX_BROWSER_HTTPS_PORT="${EDGE_PORT}" MOOX_SERVICE_HTTPS_PORT=11001 \
  "${HELPER}" ensure --deploy-dir "${TMP}/deploy" --os linux --arch amd64
assert_contains "${FAKE_LOG}" reload
assert_contains "${TMP}/deploy/config/caddy/edge.env" 'MOOX_PUBLIC_HOST=127.0.0.1'
"${HELPER}" stop --deploy-dir "${TMP}/deploy"

cp "${TMP}/candidate.Caddyfile" "${TMP}/deploy/config/caddy/Caddyfile"
printf ':broken {\n' >"${TMP}/invalid.Caddyfile"
if FAKE_VALIDATE_EXIT=1 "${HELPER}" ensure --deploy-dir "${TMP}/deploy" --os linux --arch amd64 --config "${TMP}/invalid.Caddyfile" >/dev/null 2>&1; then
  fail 'invalid candidate configuration was accepted'
fi
cmp -s "${TMP}/candidate.Caddyfile" "${TMP}/deploy/config/caddy/Caddyfile" || fail 'invalid candidate replaced the active Caddyfile'

printf '999999\n' >"${TMP}/deploy/run/caddy.pid"
"${HELPER}" stop --deploy-dir "${TMP}/deploy"
[[ ! -e "${TMP}/deploy/run/caddy.pid" ]] || fail 'stale PID file was not removed'

cp /bin/sh "${TMP}/deploy/bin/caddy"
if "${HELPER}" check --deploy-dir "${TMP}/deploy" >/dev/null 2>&1; then
  fail 'wrong managed version was accepted'
fi

printf 'PASS: pinned rootless Caddy install/start/reload/stale-pid contracts\n'
