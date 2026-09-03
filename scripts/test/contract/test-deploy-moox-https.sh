#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
HELPER="${ROOT}/scripts/lib/caddy-managed.sh"
LOOPBACK_HELPER="${ROOT}/scripts/lib/loopback-listeners.sh"
TMP=$(mktemp -d "${TMPDIR:-/tmp}/moox-caddy-managed.XXXXXX")
cleanup() { "${HELPER}" stop --deploy-dir "${TMP}/deploy" >/dev/null 2>&1 || true; rm -rf "${TMP}"; }
trap cleanup EXIT

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
assert_contains() { grep -Fq -- "$2" "$1" || fail "$1 does not contain $2"; }
assert_not_contains() { ! grep -Fq -- "$2" "$1" || fail "$1 unexpectedly contains $2"; }

[[ -x "${HELPER}" ]] || fail "managed Caddy helper is missing"
assert_contains "${ROOT}/scripts/deps/caddy-v2.11.4-checksums.txt" 'caddy_2.11.4_linux_amd64.tar.gz'
assert_contains "${ROOT}/scripts/deploy/deploy-moox.sh" 'caddy-managed.sh'
assert_contains "${ROOT}/scripts/release/release.sh" 'caddy-managed.sh'
assert_contains "${ROOT}/scripts/deploy/deploy-moox.sh" 'MOOX_WEB_HOST_ADDR:-127.0.0.1:9528'
assert_contains "${ROOT}/scripts/deploy/deploy-moox.sh" 'validate_moox_loopback_listeners'
assert_contains "${LOOPBACK_HELPER}" 'MOOX_WEB_HOST_HEALTH_ADDR-127.0.0.1:19527'
assert_contains "${LOOPBACK_HELPER}" 'MOOX_ADMIN_CONTROL_ADDR-127.0.0.1:11000'
assert_contains "${LOOPBACK_HELPER}" 'MOOX_ADMIN_SERVICE_ADDR-127.0.0.1:11002'
assert_contains "${LOOPBACK_HELPER}" 'MOOX_GATEWAY_SERVICE_ADDR-127.0.0.1:11002'
assert_contains "${LOOPBACK_HELPER}" 'MOOX_GATEWAY_HEALTH_ADDR-127.0.0.1:11012'
assert_contains "${ROOT}/scripts/deploy/deploy-moox.sh" 'moox-archive" -config=config/app.yaml -conf=config/trpc_go.yaml'
assert_contains "${ROOT}/scripts/deploy/deploy-moox.sh" 'verify_public_https'
assert_contains "${ROOT}/scripts/deploy/deploy-moox.sh" '--tls-mode <auto|public|internal>'
assert_contains "${ROOT}/scripts/deploy/deploy-moox.sh" 'resolve_tls_mode'
resolver=$(awk '/^resolve_tls_mode\(\) \{/{capture=1} capture{print} capture && /^\}/{exit}' "${ROOT}/scripts/deploy/deploy-moox.sh")
assert_equals() {
  local expected="$1" actual="$2"
  [[ "${actual}" == "${expected}" ]] || fail "expected ${expected}, got ${actual}"
}
resolve_for_test() {
  env TLS_MODE=auto PUBLIC_HOST="$1" bash -c "${resolver}; resolve_tls_mode"
}
assert_equals internal "$(resolve_for_test foo.localhost)"
assert_equals public "$(resolve_for_test 10.example.com)"
assert_equals internal "$(resolve_for_test 10.0.0.1)"
assert_equals public "$(resolve_for_test 106.53.107.122)"
assert_contains "${ROOT}/scripts/deploy/deploy-moox.sh" 'Caddyfile.public'
assert_contains "${HELPER}" 'ACME HTTP challenge port 80'
assert_contains "${ROOT}/scripts/deploy/deploy-moox.sh" 'MOOX_TLS_MODE'
assert_contains "${ROOT}/scripts/deploy/deploy-moox.sh" 'ensure_caddy'
assert_contains "${ROOT}/scripts/deploy/deploy-moox.sh" 'start_caddy'
assert_contains "${ROOT}/scripts/deploy/deploy-moox.sh" 'openssl x509 -checkend 86400'
assert_contains "${ROOT}/scripts/deploy/deploy-moox.sh" '"${ROOT}.maintenance.lock"'
assert_contains "${ROOT}/scripts/deploy/deploy-moox.sh" '"${DEPLOY_DIR}.maintenance.lock"'
assert_contains "${ROOT}/scripts/deploy/deploy-moox.sh" '"running":true'
assert_contains "${ROOT}/scripts/deploy/deploy-moox.sh" 'public HTTPS acceptance failed'
assert_contains "${ROOT}/scripts/deploy/deploy-moox.sh" 'MOOX_GATEWAY_CALLER=admin-gateway'
assert_contains "${ROOT}/scripts/deploy/deploy-moox.sh" 'canonical=$(printf "moox-gateway-auth-v1\n%s\nPOST\n%s\n\n\n%s\n%s\n%s\n%s" "$MOOX_GATEWAY_CALLER" "$path" "$body_hash" "$timestamp" "$nonce" "$node_id")'
assert_contains "${ROOT}/scripts/deploy/deploy-moox.sh" 'X-Moox-Caller: $MOOX_GATEWAY_CALLER'
assert_contains "${ROOT}/scripts/deploy/deploy-moox.sh" '--target-ca <auto|skip>'
assert_contains "${ROOT}/scripts/deploy/deploy-moox.sh" 'configure_target_ca'
assert_contains "${ROOT}/scripts/deploy/deploy-moox.sh" 'default_local_ca_output'
assert_contains "${ROOT}/scripts/deploy/deploy-moox.sh" 'local CA already trusted'
assert_contains "${ROOT}/scripts/deploy/deploy-moox.sh" 'local CA is not trusted; installing'
assert_contains "${ROOT}/scripts/deploy/deploy-moox.sh" 'MOOX_CA_SUDO_NONINTERACTIVE=1'
assert_contains "${ROOT}/scripts/deploy/deploy-moox.sh" 'skills/moox/scripts/caddy-ca.sh'
assert_contains "${ROOT}/scripts/deploy/deploy-moox.sh" 'local args=(install-target'
assert_contains "${ROOT}/scripts/deploy/deploy-moox.sh" '77)'
assert_contains "${ROOT}/scripts/deploy/deploy-moox.sh" 'target CA trust installation failed with status'
assert_contains "${ROOT}/scripts/deploy/deploy-moox.sh" 'cp "${ROOT}/scripts/deploy/install-caddy-ca.sh" "${STAGE_DIR}/lib/install-caddy-ca.sh"'
assert_contains "${ROOT}/scripts/deploy/deploy-moox.sh" 'cp "${ROOT}/scripts/lib/loopback-listeners.sh" "${STAGE_DIR}/lib/loopback-listeners.sh"'
assert_contains "${HELPER}" '.activation.rollback'
assert_contains "${HELPER}" 'MOOX_TLS_MODE'
assert_contains "${HELPER}" '"tls_mode":"%s"'
assert_contains "${HELPER}" 'certs/caddy/root.crt'
assert_contains "${HELPER}" 'restore_file "${ENV_FILE}"'
assert_contains "${HELPER}" 'restore_file "${CA_ROOT}"'
assert_contains "${HELPER}" 'restore_file "${CA_SHA}"'
assert_contains "${ROOT}/scripts/deploy/deploy-moox.sh" 'Caddyfile.next'
assert_contains "${ROOT}/scripts/deploy/deploy-moox.sh" '--config "${deploy_dir}/config/caddy/Caddyfile.next"'
assert_contains "${ROOT}/scripts/deploy/deploy-moox.sh" 'find "${DEPLOY_DIR}/data" -mindepth 1 -maxdepth 1 ! -name caddy'
assert_not_contains "${ROOT}/scripts/deploy/deploy-moox.sh" 'mv "${DEPLOY_DIR}/data/caddy"'
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
  reload) printf 'reload\n' >>"${FAKE_LOG}"; exit "${FAKE_RELOAD_EXIT:-0}" ;;
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
if [[ "$1" == -n && "$2" == setcap && "$3" == cap_net_bind_service=+ep ]]; then
  printf '%s\n' "$4" >"${FAKE_CAP_STATE}"
elif [[ "$1" == -n && "$2" == setcap && "$3" == -r ]]; then
  rm -f "${FAKE_CAP_STATE}"
else
  exit 2
fi
SH
cat >"${TMP}/cap-bin/lsof" <<'SH'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"${FAKE_PORT_LOG}"
fake_pid() {
  for pid_state in ${FAKE_PID_STATES:-${FAKE_PID_STATE:-}}; do
    if [[ -s "${pid_state}" ]]; then
      cat "${pid_state}"
      return 0
    fi
  done
  return 1
}
if [[ "${FAKE_OCCUPY_9527:-0}" == 1 && "$*" == *TCP:9527* ]]; then
  printf '424242\n'
  exit 0
fi
if [[ "${FAKE_OCCUPY_80:-0}" == 1 && "$*" == *TCP:80* ]]; then
  printf '424243\n'
  exit 0
fi
if [[ "$*" == *"TCP:${FAKE_ADMIN_PORT}"* ]] && fake_pid; then
  exit 0
fi
for managed_port in ${FAKE_MANAGED_PORTS:-}; do
  if [[ "$*" == *"TCP:${managed_port}"* ]] && fake_pid; then
    exit 0
  fi
done
[[ -z "${REAL_LSOF:-}" ]] || exec "${REAL_LSOF}" "$@"
SH
chmod +x "${TMP}/cap-bin/getcap" "${TMP}/cap-bin/sudo" "${TMP}/cap-bin/lsof"

CAP_DEPLOY="${TMP}/cap-deploy"
CAP_STATE="${TMP}/cap.state"
SUDO_LOG="${TMP}/sudo.log"
PORT_LOG="${TMP}/ports.log"
REAL_LSOF=$(command -v lsof || true)
export FAKE_PORT_LOG="${PORT_LOG}"
export REAL_LSOF
export FAKE_CAP_STATE="${CAP_STATE}" FAKE_SUDO_LOG="${SUDO_LOG}"
export PATH="${TMP}/cap-bin:${PATH}"
FAKE_CAP_STATE="${CAP_STATE}" FAKE_SUDO_LOG="${SUDO_LOG}" PATH="${TMP}/cap-bin:${PATH}" \
MOOX_CADDY_ARCHIVE="${TMP}/caddy_2.11.4_linux_amd64.tar.gz" MOOX_CADDY_CHECKSUMS="${TMP}/checksums.txt" \
  "${HELPER}" install --deploy-dir "${CAP_DEPLOY}" --os linux --arch amd64 --ports 443
grep -Fxq -- '-n setcap cap_net_bind_service=+ep '"${CAP_DEPLOY}/bin/caddy" "${SUDO_LOG}" || \
  fail 'privileged-port install did not grant only cap_net_bind_service'

# An existing version-correct binary must recover a missing capability, and a
# subsequent binary replacement must grant it again.
mkdir -p "${CAP_DEPLOY}/config/caddy"
FAKE_CAP_STATE="${CAP_STATE}" FAKE_SUDO_LOG="${SUDO_LOG}" PATH="${TMP}/cap-bin:${PATH}" \
  "${HELPER}" ensure --deploy-dir "${CAP_DEPLOY}" --os linux --arch amd64 --ports 443
grep -Fq 'MOOX_CADDY_PORTS=443' "${CAP_DEPLOY}/config/caddy/edge.env" || \
  fail 'ensure did not persist the exact configured Caddy port set'
rm -f "${CAP_STATE}"; : >"${SUDO_LOG}"
FAKE_CAP_STATE="${CAP_STATE}" FAKE_SUDO_LOG="${SUDO_LOG}" PATH="${TMP}/cap-bin:${PATH}" \
  "${HELPER}" ensure --deploy-dir "${CAP_DEPLOY}" --os linux --arch amd64
[[ -s "${CAP_STATE}" ]] || fail 'existing managed Caddy did not recover its privileged-port capability'
rm -f "${CAP_STATE}"; : >"${SUDO_LOG}"
FAKE_CAP_STATE="${CAP_STATE}" FAKE_SUDO_LOG="${SUDO_LOG}" PATH="${TMP}/cap-bin:${PATH}" \
MOOX_CADDY_ARCHIVE="${TMP}/caddy_2.11.4_linux_amd64.tar.gz" MOOX_CADDY_CHECKSUMS="${TMP}/checksums.txt" \
  "${HELPER}" install --deploy-dir "${CAP_DEPLOY}" --os linux --arch amd64 --ports 443
[[ -s "${CAP_STATE}" ]] || fail 'managed Caddy replacement did not reapply its privileged-port capability'

# Dropping the last privileged port must also drop the file capability. Once
# absent, an unprivileged-only configuration must not use sudo again.
: >"${SUDO_LOG}"
FAKE_CAP_STATE="${CAP_STATE}" FAKE_SUDO_LOG="${SUDO_LOG}" PATH="${TMP}/cap-bin:${PATH}" \
  "${HELPER}" ensure --deploy-dir "${CAP_DEPLOY}" --os linux --arch amd64 --ports 8443
[[ ! -e "${CAP_STATE}" ]] || fail 'unprivileged-only Caddy retained cap_net_bind_service'
grep -Fxq -- '-n setcap -r '"${CAP_DEPLOY}/bin/caddy" "${SUDO_LOG}" || \
  fail 'privileged-to-unprivileged transition did not narrowly remove file capabilities'
: >"${SUDO_LOG}"

# Explicit deployment edge values must win over an older persisted edge.env.
printf 'MOOX_CADDY_PORTS=9527\\,443\nMOOX_SERVICE_HTTPS_PORT=443\n' >"${CAP_DEPLOY}/config/caddy/edge.env"
MOOX_PUBLIC_HOST=127.0.0.1 MOOX_BROWSER_HTTPS_PORT=9527 MOOX_SERVICE_HTTPS_PORT=11001 MOOX_TLS_MODE=public \
MOOX_CADDY_SKIP_CA_WAIT=1 FAKE_CAP_STATE="${CAP_STATE}" FAKE_SUDO_LOG="${SUDO_LOG}" PATH="${TMP}/cap-bin:${PATH}" \
  "${HELPER}" ensure --deploy-dir "${CAP_DEPLOY}" --os linux --arch amd64 --ports 8443
[[ -s "${CAP_STATE}" ]] || fail 'public TLS did not grant the capability required for transient HTTP-01 challenges'
grep -Fxq 'MOOX_CADDY_PORTS=8443' "${CAP_DEPLOY}/config/caddy/edge.env" || \
  fail 'explicit Caddy ports were overridden by persisted edge.env'
grep -Fxq 'MOOX_SERVICE_HTTPS_PORT=11001' "${CAP_DEPLOY}/config/caddy/edge.env" || \
  fail 'explicit Caddy service port was overridden by persisted edge.env'
grep -Fxq 'MOOX_TLS_MODE=public' "${CAP_DEPLOY}/config/caddy/edge.env" || \
  fail 'public TLS mode was not persisted for renewal and lifecycle commands'
: >"${SUDO_LOG}"

FAKE_CAP_STATE="${CAP_STATE}" FAKE_SUDO_LOG="${SUDO_LOG}" PATH="${TMP}/cap-bin:${PATH}" \
  "${HELPER}" ensure --deploy-dir "${CAP_DEPLOY}" --os linux --arch amd64
! grep -Fq 'setcap' "${SUDO_LOG}" || fail 'persisted public TLS capability was needlessly changed'
printf ':8443 { respond "ok" }\n' >"${CAP_DEPLOY}/config/caddy/Caddyfile"
if FAKE_CAP_STATE="${CAP_STATE}" FAKE_SUDO_LOG="${SUDO_LOG}" FAKE_OCCUPY_80=1 PATH="${TMP}/cap-bin:${PATH}" \
  "${HELPER}" check --deploy-dir "${CAP_DEPLOY}" --os linux --arch amd64 >/dev/null 2>"${TMP}/port-80.out"; then
  fail 'public TLS accepted an unrelated listener on ACME port 80'
fi
grep -Fq 'ACME HTTP challenge port 80' "${TMP}/port-80.out" || fail 'ACME port conflict failure was unclear'
rm -f "${CAP_DEPLOY}/config/caddy/Caddyfile"

# Port syntax is platform-independent; non-Linux never mutates capabilities.
printf '%s\n' "${CAP_DEPLOY}/bin/caddy" >"${CAP_STATE}"; : >"${SUDO_LOG}"
FAKE_CAP_STATE="${CAP_STATE}" FAKE_SUDO_LOG="${SUDO_LOG}" PATH="${TMP}/cap-bin:${PATH}" \
  "${HELPER}" ensure --deploy-dir "${CAP_DEPLOY}" --os darwin --arch amd64 --ports 443
[[ -s "${CAP_STATE}" && ! -s "${SUDO_LOG}" ]] || fail 'non-Linux Caddy mutated Linux file capabilities'
for os in linux darwin; do
  for invalid_ports in '' 0 65536 0443 +443 '443,' ',443' '443,,8443' '443, 8443' abc; do
    if FAKE_CAP_STATE="${CAP_STATE}" FAKE_SUDO_LOG="${SUDO_LOG}" PATH="${TMP}/cap-bin:${PATH}" \
      "${HELPER}" ensure --deploy-dir "${CAP_DEPLOY}" --os "${os}" --arch amd64 --ports "${invalid_ports}" \
        >"${TMP}/invalid-port.out" 2>&1; then
      fail "${os} accepted invalid Caddy ports: '${invalid_ports}'"
    fi
    grep -Fq 'invalid Caddy ports' "${TMP}/invalid-port.out" || fail "invalid port failure was unclear: '${invalid_ports}'"
  done
done

# A no-Admin deployment persists only 443. Later lifecycle commands must not
# synthesize browser port 9527 and collide with an unrelated listener there.
: >"${SUDO_LOG}"; : >"${PORT_LOG}"
FAKE_CAP_STATE="${CAP_STATE}" FAKE_SUDO_LOG="${SUDO_LOG}" PATH="${TMP}/cap-bin:${PATH}" \
  "${HELPER}" ensure --deploy-dir "${CAP_DEPLOY}" --os linux --arch amd64 --ports 443
printf ':443 { respond "ok" }\n' >"${CAP_DEPLOY}/config/caddy/Caddyfile"
FAKE_CAP_STATE="${CAP_STATE}" FAKE_SUDO_LOG="${SUDO_LOG}" PATH="${TMP}/cap-bin:${PATH}" \
FAKE_OCCUPY_9527=1 "${HELPER}" check --deploy-dir "${CAP_DEPLOY}" --os linux --arch amd64
grep -Fq 'TCP:443' "${PORT_LOG}" || fail 'persisted no-Admin service port was not checked'
! grep -Fq 'TCP:9527' "${PORT_LOG}" || fail 'persisted no-Admin port set reintroduced browser port 9527'

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
export FAKE_PID_STATES="${TMP}/deploy/run/caddy.pid ${TMP}/first-deploy/run/caddy.pid"
export FAKE_MANAGED_PORTS='443 8443 9527 11001'
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

printf ':443 { respond "active-443" }\n' >"${TMP}/tx-443.Caddyfile"
printf ':8443 { respond "active-8443" }\n' >"${TMP}/tx-8443.Caddyfile"
printf ':broken {\n' >"${TMP}/tx-invalid.Caddyfile"
"${HELPER}" ensure --deploy-dir "${TMP}/deploy" --os linux --arch amd64 \
  --ports 443 --config "${TMP}/tx-443.Caddyfile"
grep -Fq 'active-443' "${TMP}/deploy/config/caddy/Caddyfile" || fail '443 baseline config was not activated'
grep -Fq 'MOOX_CADDY_PORTS=443' "${TMP}/deploy/config/caddy/edge.env" || fail '443 baseline ports were not activated'
[[ -s "${CAP_STATE}" ]] || fail '443 baseline capability was not activated'
cp "${TMP}/deploy/config/caddy/Caddyfile" "${TMP}/expected-443.Caddyfile"
cp "${TMP}/deploy/config/caddy/edge.env" "${TMP}/expected-443.env"

# Candidate validation must happen before any active config, ports, or
# capability mutation in either direction.
if FAKE_VALIDATE_EXIT=1 "${HELPER}" ensure --deploy-dir "${TMP}/deploy" --os linux --arch amd64 \
  --ports 8443 --config "${TMP}/tx-invalid.Caddyfile" >/dev/null 2>&1; then
  fail 'invalid 443-to-8443 candidate was accepted'
fi
grep -Fq 'active-443' "${TMP}/deploy/config/caddy/Caddyfile" || fail 'validation failure replaced the 443 config'
grep -Fq 'MOOX_CADDY_PORTS=443' "${TMP}/deploy/config/caddy/edge.env" || fail 'validation failure replaced the 443 port set'
cmp -s "${TMP}/expected-443.Caddyfile" "${TMP}/deploy/config/caddy/Caddyfile" || fail 'validation failure did not restore the exact 443 config'
cmp -s "${TMP}/expected-443.env" "${TMP}/deploy/config/caddy/edge.env" || fail 'validation failure did not restore the exact 443 environment'
[[ -s "${CAP_STATE}" ]] || fail 'validation failure removed the 443 capability'

# Reload failure must atomically restore the old activation, after which a
# cold start from restored files must still work.
if FAKE_RELOAD_EXIT=1 "${HELPER}" ensure --deploy-dir "${TMP}/deploy" --os linux --arch amd64 \
  --ports 8443 --config "${TMP}/tx-8443.Caddyfile" >/dev/null 2>&1; then
  fail '443-to-8443 activation unexpectedly survived reload failure'
fi
grep -Fq 'active-443' "${TMP}/deploy/config/caddy/Caddyfile" || fail 'reload failure did not restore the 443 config'
grep -Fq 'MOOX_CADDY_PORTS=443' "${TMP}/deploy/config/caddy/edge.env" || fail 'reload failure did not restore the 443 port set'
cmp -s "${TMP}/expected-443.Caddyfile" "${TMP}/deploy/config/caddy/Caddyfile" || fail 'reload failure did not restore the exact 443 config'
cmp -s "${TMP}/expected-443.env" "${TMP}/deploy/config/caddy/edge.env" || fail 'reload failure did not restore the exact 443 environment'
[[ -s "${CAP_STATE}" ]] || fail 'reload failure did not restore the 443 capability'
"${HELPER}" stop --deploy-dir "${TMP}/deploy" --os linux --arch amd64
"${HELPER}" start --deploy-dir "${TMP}/deploy" --os linux --arch amd64

"${HELPER}" ensure --deploy-dir "${TMP}/deploy" --os linux --arch amd64 \
  --ports 8443 --config "${TMP}/tx-8443.Caddyfile"
grep -Fq 'active-8443' "${TMP}/deploy/config/caddy/Caddyfile" || fail '8443 baseline config was not activated'
grep -Fq 'MOOX_CADDY_PORTS=8443' "${TMP}/deploy/config/caddy/edge.env" || fail '8443 baseline ports were not activated'
[[ ! -e "${CAP_STATE}" ]] || fail '8443 baseline retained the bind capability'
cp "${TMP}/deploy/config/caddy/Caddyfile" "${TMP}/expected-8443.Caddyfile"
cp "${TMP}/deploy/config/caddy/edge.env" "${TMP}/expected-8443.env"

if FAKE_VALIDATE_EXIT=1 "${HELPER}" ensure --deploy-dir "${TMP}/deploy" --os linux --arch amd64 \
  --ports 443 --config "${TMP}/tx-invalid.Caddyfile" >/dev/null 2>&1; then
  fail 'invalid 8443-to-443 candidate was accepted'
fi
grep -Fq 'active-8443' "${TMP}/deploy/config/caddy/Caddyfile" || fail 'validation failure replaced the 8443 config'
grep -Fq 'MOOX_CADDY_PORTS=8443' "${TMP}/deploy/config/caddy/edge.env" || fail 'validation failure replaced the 8443 port set'
cmp -s "${TMP}/expected-8443.Caddyfile" "${TMP}/deploy/config/caddy/Caddyfile" || fail 'validation failure did not restore the exact 8443 config'
cmp -s "${TMP}/expected-8443.env" "${TMP}/deploy/config/caddy/edge.env" || fail 'validation failure did not restore the exact 8443 environment'
[[ ! -e "${CAP_STATE}" ]] || fail 'validation failure granted the 443 capability'

if FAKE_RELOAD_EXIT=1 "${HELPER}" ensure --deploy-dir "${TMP}/deploy" --os linux --arch amd64 \
  --ports 443 --config "${TMP}/tx-443.Caddyfile" >/dev/null 2>&1; then
  fail '8443-to-443 activation unexpectedly survived reload failure'
fi
grep -Fq 'active-8443' "${TMP}/deploy/config/caddy/Caddyfile" || fail 'reload failure did not restore the 8443 config'
grep -Fq 'MOOX_CADDY_PORTS=8443' "${TMP}/deploy/config/caddy/edge.env" || fail 'reload failure did not restore the 8443 port set'
cmp -s "${TMP}/expected-8443.Caddyfile" "${TMP}/deploy/config/caddy/Caddyfile" || fail 'reload failure did not restore the exact 8443 config'
cmp -s "${TMP}/expected-8443.env" "${TMP}/deploy/config/caddy/edge.env" || fail 'reload failure did not restore the exact 8443 environment'
[[ ! -e "${CAP_STATE}" ]] || fail 'reload failure retained the rejected 443 capability'
"${HELPER}" stop --deploy-dir "${TMP}/deploy" --os linux --arch amd64
"${HELPER}" start --deploy-dir "${TMP}/deploy" --os linux --arch amd64

# Explicit rollback uses the same complete snapshot and reconciles the restored
# binary to the restored port set. Legacy internal edge.env files did not
# include MOOX_TLS_MODE, so absence must still trigger an internal-to-public
# cold start.
grep -v '^MOOX_TLS_MODE=' "${TMP}/deploy/config/caddy/edge.env" >"${TMP}/deploy/config/caddy/edge.env.legacy"
mv "${TMP}/deploy/config/caddy/edge.env.legacy" "${TMP}/deploy/config/caddy/edge.env"
cp "${TMP}/deploy/config/caddy/edge.env" "${TMP}/expected-8443.env"
mkdir -p "${TMP}/deploy/certs/caddy"
printf 'old-root\n' >"${TMP}/deploy/certs/caddy/root.crt"
printf 'old-sha\n' >"${TMP}/deploy/certs/caddy/root.sha256"
MOOX_PUBLIC_HOST=127.0.0.1 MOOX_TLS_MODE=public "${HELPER}" ensure --deploy-dir "${TMP}/deploy" --os linux --arch amd64 \
  --ports 443 --config "${TMP}/tx-443.Caddyfile"
public_pid=$(cat "${TMP}/deploy/run/caddy.pid")
[[ ! -e "${TMP}/deploy/certs/caddy/root.crt" && ! -e "${TMP}/deploy/certs/caddy/root.sha256" ]] || \
  fail 'public activation retained stale private CA artifacts'
"${HELPER}" rollback --deploy-dir "${TMP}/deploy" --os linux --arch amd64
rollback_pid=$(cat "${TMP}/deploy/run/caddy.pid")
[[ "${rollback_pid}" != "${public_pid}" ]] || fail 'TLS mode rollback reused the public-mode Caddy process'
kill -0 "${rollback_pid}" || fail 'TLS mode rollback did not cold-start the restored internal activation'
cmp -s "${TMP}/expected-8443.Caddyfile" "${TMP}/deploy/config/caddy/Caddyfile" || fail 'explicit rollback did not restore the exact 8443 config'
cmp -s "${TMP}/expected-8443.env" "${TMP}/deploy/config/caddy/edge.env" || fail 'explicit rollback did not restore the exact 8443 environment'
[[ ! -e "${CAP_STATE}" ]] || fail 'explicit rollback did not remove the rejected 443 capability'
grep -Fxq 'old-root' "${TMP}/deploy/certs/caddy/root.crt" || fail 'explicit rollback did not restore the private root'
grep -Fxq 'old-sha' "${TMP}/deploy/certs/caddy/root.sha256" || fail 'explicit rollback did not restore the private root fingerprint'

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
