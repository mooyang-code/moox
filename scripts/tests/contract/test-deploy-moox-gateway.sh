#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
DEPLOY="${ROOT}/scripts/deploy-moox.sh"
BUILD="${ROOT}/scripts/build.sh"
RELEASE="${ROOT}/scripts/release.sh"
CADDY="${ROOT}/deploy/caddy/Caddyfile"
NO_ADMIN_CADDY="${ROOT}/deploy/caddy/Caddyfile.no-admin"

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
assert_contains() { grep -Fq -- "$2" "$1" || fail "$1 does not contain: $2"; }
assert_absent() { ! grep -Fq -- "$2" "$1" || fail "$1 unexpectedly contains: $2"; }
file_mode() {
  local mode
  if mode=$(stat -f '%Lp' "$1" 2>/dev/null); then
    printf '%s\n' "${mode}"
    return
  fi
  stat -c '%a' "$1"
}

assert_contains "${BUILD}" 'gateway)'
assert_contains "${BUILD}" 'modules/gateway ./cmd/server moox-gateway'
assert_contains "${BUILD}" 'modules/gateway ./cmd/cli moox-gateway-cli'
assert_contains "${RELEASE}" 'gateway/config'
assert_contains "${RELEASE}" 'gateway/config/trpc_go.yaml'
assert_contains "${RELEASE}" 'obsolete Gateway refresh_interval'
assert_contains "${RELEASE}" 'copy_binary moox-gateway'
assert_contains "${RELEASE}" 'copy_binary moox-gateway-cli'

for option in --node-id --gateway-control-url --gateway-ca-bundle --gateway-control-key-file --gateway-service-key-file --monitor-instance-id --no-admin; do
  assert_contains "${DEPLOY}" "${option}"
done
assert_absent "${DEPLOY}" '--monitor-peer'
assert_contains "${DEPLOY}" 'MOOX_ADMIN_NODE_ID'
assert_contains "${DEPLOY}" 'MOOX_GATEWAY_NODE_ID'
assert_contains "${DEPLOY}" 'gateway-control.env'
assert_contains "${DEPLOY}" 'gateway-service.env'
assert_contains "${DEPLOY}" 'chmod 0600'
assert_contains "${DEPLOY}" 'certs/gateway/peers.pem'
assert_contains "${DEPLOY}" 'start_gateway'
assert_contains "${DEPLOY}" 'WITH_GATEWAY=${quoted_with_gateway}'
assert_contains "${DEPLOY}" 'moox-gateway" -config=config/app.yaml -conf=config/trpc_go.yaml'
assert_contains "${DEPLOY}" 'start_admin'
assert_contains "${DEPLOY}" 'start_monitor'
assert_contains "${DEPLOY}" 'gateway) url=http://127.0.0.1:11012/readyz'
assert_contains "${DEPLOY}" 'start_service "gateway"'
assert_contains "${DEPLOY}" 'stop_service "gateway"'
assert_contains "${DEPLOY}" 'services+=(gateway)'
assert_contains "${DEPLOY}" '[[ "${WITH_EVENTBUS}" == "1" ]] && "${DEPLOY_DIR}/stop.sh" eventbus || true'
assert_contains "${DEPLOY}" '[[ "${WITH_GATEWAY}" == "1" ]] && "${DEPLOY_DIR}/stop.sh" gateway || true'
assert_absent "${DEPLOY}" '[[ "${WITH_EVENTBUS}" == "1" ]] || disabled_services+=(eventbus)'
assert_absent "${DEPLOY}" '[[ "${WITH_GATEWAY}" == "1" ]] || disabled_services+=(moox_gateway)'
assert_absent "${DEPLOY}" 'source "${ROOT}/secrets/gateway-control.env"'
assert_absent "${DEPLOY}" 'source "${ROOT}/secrets/gateway-service.env"'
assert_contains "${DEPLOY}" 'env "${RUNTIME_IDENTITY_ENV[@]}" "${ADMIN_SECRET_ENV[@]}"'
assert_contains "${DEPLOY}" 'gateway_service_env_for monitor'
assert_contains "${DEPLOY}" 'LOCAL_DEPLOY_ARCHIVE=$(mktemp'
assert_contains "${DEPLOY}" 'REMOTE_DEPLOY_ARCHIVE'
assert_contains "${DEPLOY}" 'cleanup_deploy_artifacts'

production_contract_paths=(
  "${DEPLOY}"
  "${RELEASE}"
  "${ROOT}/scripts/build.sh"
  "${ROOT}/skills/moox/scripts/cls-bootstrap.sh"
  "${ROOT}/deploy"
)
while IFS= read -r config_dir; do
  production_contract_paths+=("${config_dir}")
done < <(find "${ROOT}/modules" -type d -name config -print)
if obsolete_refs=$(rg -n 'service-auth\.env|LEGACY_SERVICE_ENV|MOOX_SERVICE_AUTH_' "${production_contract_paths[@]}" 2>/dev/null); then
  printf '%s\n' "${obsolete_refs}" >&2
  fail 'production scripts or config still reference the obsolete service-auth contract'
fi

assert_contains "${CADDY}" 'handle /api/gateway-control/*'
assert_contains "${CADDY}" 'reverse_proxy 127.0.0.1:11000'
assert_contains "${CADDY}" 'handle /api/service/*'
assert_contains "${CADDY}" 'reverse_proxy 127.0.0.1:11002'
[[ -f "${NO_ADMIN_CADDY}" ]] || fail 'no-admin Caddyfile is missing'
assert_absent "${NO_ADMIN_CADDY}" '/api/gateway-control/'
assert_absent "${NO_ADMIN_CADDY}" 'MOOX_BROWSER_HTTPS_PORT'
assert_contains "${NO_ADMIN_CADDY}" 'handle /api/service/*'
assert_contains "${NO_ADMIN_CADDY}" 'reverse_proxy 127.0.0.1:11002'

assert_contains "${ROOT}/modules/gateway/config/trpc_go.yaml" 'trpc.moox.gateway.route_refresh.timer'
assert_contains "${ROOT}/modules/gateway/config/trpc_go.yaml" 'port: 11013'
assert_absent "${ROOT}/modules/gateway/config/trpc_go.yaml" 'startAtOnce'
assert_absent "${ROOT}/modules/gateway/config/app.yaml" 'refresh_interval'

command -v openssl >/dev/null 2>&1 || fail 'openssl is required for the deployment fixture'
TMP=$(mktemp -d "${TMPDIR:-/tmp}/moox-gateway-deploy.XXXXXX")
CREATED_BINARIES=()
TEST_PIDS=()
cleanup() {
  local binary pid
  for pid in ${TEST_PIDS[@]+"${TEST_PIDS[@]}"}; do kill "${pid}" 2>/dev/null || true; done
  for pid in ${TEST_PIDS[@]+"${TEST_PIDS[@]}"}; do wait "${pid}" 2>/dev/null || true; done
  for binary in ${CREATED_BINARIES[@]+"${CREATED_BINARIES[@]}"}; do rm -f "${binary}"; done
  rm -rf "${TMP}"
}
trap cleanup EXIT
mkdir -p "${ROOT}/bin"
for name in moox-gateway moox-gateway-cli moox-cli moox-monitor moox-monitor-cli moox-eventbus; do
  binary="${ROOT}/bin/${name}"
  if [[ ! -x "${binary}" ]]; then
    printf '#!/usr/bin/env bash\nexit 0\n' >"${binary}"
    chmod +x "${binary}"
    CREATED_BINARIES+=("${binary}")
  fi
done
printf 'control-secret' >"${TMP}/control.key"
printf 'service-secret' >"${TMP}/service.key"
chmod 0600 "${TMP}/control.key" "${TMP}/service.key"
REAL_STAT_BIN=$(command -v stat)
mkdir -p "${TMP}/gnu-stat-bin"
cat >"${TMP}/gnu-stat-bin/stat" <<'SH'
#!/usr/bin/env bash
if [[ "${1:-}" == -f && "${2:-}" == %Lp ]]; then
  printf 'GNU stat diagnostic output before rejecting the BSD format\n'
  exit 1
fi
if [[ "${1:-}" == -c && "${2:-}" == %a ]]; then
  printf '600\n'
  exit 0
fi
exec "${REAL_STAT_BIN}" "$@"
SH
chmod +x "${TMP}/gnu-stat-bin/stat"
openssl req -x509 -newkey rsa:2048 -nodes -subj /CN=gateway-test \
  -keyout "${TMP}/private-one.key" -out "${TMP}/root-one.crt" -days 1 >/dev/null 2>&1
openssl req -x509 -newkey rsa:2048 -nodes -subj /CN=gateway-peer-test \
  -keyout "${TMP}/private-two.key" -out "${TMP}/root-two.crt" -days 1 >/dev/null 2>&1
cat "${TMP}/root-one.crt" "${TMP}/root-two.crt" >"${TMP}/peers.pem"

deploy_fixture() {
  local bundle="$1" suffix="$2" control_url="${3:-https://admin.example.com:9527/}"
  "${DEPLOY}" --target localhost --dir "${TMP}/deploy-${suffix}" --stage "${TMP}/stage-${suffix}" \
    --skip-build --no-start --no-admin --no-storage --no-archive --no-eventbus \
    --no-cloudnode --no-collector --no-factor --no-strategy --no-trade --no-monitor --local-ca skip --target-ca skip \
    --node-id gateway-test --gateway-control-url "${control_url}" \
    --gateway-ca-bundle "${bundle}" --gateway-control-key-file "${TMP}/control.key" \
    --gateway-service-key-file "${TMP}/service.key"
}

expect_ca_rejected() {
  local bundle="$1" suffix="$2" message="$3" output
  if output=$(deploy_fixture "${bundle}" "${suffix}" 2>&1); then
    fail "Gateway deployment accepted ${suffix} CA bundle"
  fi
  grep -Fq -- "${message}" <<<"${output}" || fail "${suffix} CA rejection did not explain: ${message}"
}

expect_url_rejected() {
  local value="$1" suffix="$2" output
  if output=$(deploy_fixture "${TMP}/peers.pem" "url-${suffix}" "${value}" 2>&1); then
    fail "Gateway deployment accepted invalid control URL: ${value}"
  fi
  grep -Fq -- '--gateway-control-url must be HTTPS, or loopback HTTP' <<<"${output}" || \
    fail "invalid control URL rejection was unclear: ${value}"
}

cat "${TMP}/root-one.crt" "${TMP}/root-one.crt" >"${TMP}/duplicate.pem"
cat "${TMP}/root-one.crt" >"${TMP}/malformed.pem"
printf '%s\n' '-----BEGIN CERTIFICATE-----' 'not-a-certificate' '-----END CERTIFICATE-----' >>"${TMP}/malformed.pem"
cat "${TMP}/peers.pem" "${TMP}/private-one.key" >"${TMP}/private.pem"
expect_ca_rejected "${TMP}/duplicate.pem" duplicate 'at least two distinct public CA certificates'
expect_ca_rejected "${TMP}/malformed.pem" malformed 'contains a malformed certificate'
expect_ca_rejected "${TMP}/private.pem" private 'must never contain private-key blocks'
if output=$(REAL_STAT_BIN="${REAL_STAT_BIN}" PATH="${TMP}/gnu-stat-bin:${PATH}" \
  deploy_fixture "${TMP}/duplicate.pem" duplicate-gnu-stat 2>&1); then
  fail 'Gateway deployment accepted a duplicate CA bundle with GNU stat semantics'
fi
grep -Fq -- 'at least two distinct public CA certificates' <<<"${output}" || \
  fail 'GNU stat fallback corrupted the detected secret-file mode'
expect_url_rejected 'http://example.com:11000' remote-http
expect_url_rejected 'https://user:pass@example.com' credentials
expect_url_rejected 'https://example.com?node=other' query
expect_url_rejected 'https://example.com/#fragment' fragment
expect_url_rejected $'https://example.com\ninvalid' whitespace
expect_url_rejected 'https://%65xample.com' escaped-authority
expect_url_rejected 'https://example.com:' empty-port
expect_url_rejected 'https://[::1' malformed-ipv6

deploy_fixture "${TMP}/peers.pem" valid >/dev/null
DEPLOYED="${TMP}/deploy-valid"

for script in start.sh stop.sh restart.sh status.sh healthcheck.sh; do
  bash -n "${DEPLOYED}/${script}" || fail "generated ${script} is invalid"
done
[[ -x "${DEPLOYED}/bin/moox-gateway" ]] || fail 'Gateway binary was not staged'
[[ -f "${DEPLOYED}/gateway/config/app.yaml" ]] || fail 'Gateway config was not staged'
[[ -f "${DEPLOYED}/gateway/config/trpc_go.yaml" ]] || fail 'Gateway tRPC Timer config was not staged'
grep -Fq 'id: gateway-test' "${DEPLOYED}/gateway/config/app.yaml" || fail 'node ID was not rendered'
grep -Fq 'base_url: "https://admin.example.com:9527/"' "${DEPLOYED}/gateway/config/app.yaml" || fail 'control URL was not safely rendered as YAML'
[[ ! -e "${DEPLOYED}/admin" && ! -e "${DEPLOYED}/bin/moox-admin" ]] || fail 'no-admin staged Admin artifacts'
[[ ! -e "${DEPLOYED}/bin/moox-web-host" && ! -e "${DEPLOYED}/secrets/admin-jwt.env" ]] || fail 'no-admin staged browser or Admin credentials'
[[ ! -e "${DEPLOYED}/secrets/service-auth.env" ]] || fail 'deployment generated obsolete service-auth credentials'
[[ -f "${DEPLOYED}/secrets/gateway-credentials.json" ]] || fail 'Gateway credential registry was not staged'
grep -Fq 'credentials_file: ../../secrets/gateway-credentials.json' "${DEPLOYED}/gateway/config/app.yaml" || fail 'Gateway credential registry was not configured'
cmp -s "${TMP}/peers.pem" "${DEPLOYED}/certs/gateway/peers.pem" || fail 'public peer CA was not installed'
! grep -Rqs -- 'PRIVATE KEY' "${DEPLOYED}/certs" || fail 'a private CA key was staged'
for secret in gateway-control.env gateway-service.env gateway-control.key gateway-service.key; do
  mode=$(file_mode "${DEPLOYED}/secrets/${secret}")
  [[ "${mode}" == 600 ]] || fail "${secret} mode is ${mode}, want 600"
done
grep -Fq 'MOOX_GATEWAY_NODE_ID=gateway-test' "${DEPLOYED}/secrets/gateway-service.env" || fail 'Gateway node ID was not scoped with service credentials'
grep -Fq 'start_admin' "${DEPLOYED}/start.sh" || fail 'central lifecycle function was omitted'
grep -Fq 'start_gateway' "${DEPLOYED}/start.sh" || fail 'Gateway lifecycle function was omitted'
grep -Fq 'start_monitor' "${DEPLOYED}/start.sh" || fail 'Monitor lifecycle function was omitted'
admin_line=$(grep -n 'start_admin$' "${DEPLOYED}/start.sh" | tail -1 | cut -d: -f1)
gateway_line=$(grep -n 'start_gateway$' "${DEPLOYED}/start.sh" | head -1 | cut -d: -f1)
monitor_line=$(grep -n 'start_monitor$' "${DEPLOYED}/start.sh" | tail -1 | cut -d: -f1)
(( admin_line < gateway_line && gateway_line < monitor_line )) || fail 'startup order is not Admin -> Gateway -> Monitor'

# A reused PID must never cause the lifecycle scripts to kill an unrelated process.
sleep 30 &
unrelated_pid=$!
TEST_PIDS+=("${unrelated_pid}")
mkdir -p "${DEPLOYED}/run"
printf '%s\n' "${unrelated_pid}" >"${DEPLOYED}/run/gateway.pid"
"${DEPLOYED}/stop.sh" gateway >/dev/null
kill -0 "${unrelated_pid}" 2>/dev/null || fail 'Gateway stop killed an unrelated reused PID'
[[ ! -e "${DEPLOYED}/run/gateway.pid" ]] || fail 'stale reused Gateway PID file was not removed'

# A no-Admin Monitor node still needs moox-cli for metadata apply, and only
# Monitor (not Gateway) may inherit the cluster service credential.
MOOX_EVENTBUS_ENABLE_TLS=1 "${DEPLOY}" --target localhost --dir "${TMP}/deploy-monitor" --stage "${TMP}/stage-monitor" \
  --skip-build --no-start --no-admin --no-storage --no-archive \
  --no-cloudnode --no-collector --no-factor --no-strategy --no-trade --local-ca skip --target-ca skip \
  --node-id gateway-monitor --gateway-control-url 'http://[::1]:11000' \
  --monitor-instance-id monitor-local \
  --gateway-ca-bundle "${TMP}/peers.pem" --gateway-control-key-file "${TMP}/control.key" \
  --gateway-service-key-file "${TMP}/service.key" >/dev/null
MONITOR_DEPLOY="${TMP}/deploy-monitor"
[[ -x "${MONITOR_DEPLOY}/bin/moox-cli" ]] || fail 'no-admin Monitor package omitted moox-cli'
grep -Fq 'instance_id: "monitor-local"' "${MONITOR_DEPLOY}/monitor/config/app.yaml" || fail 'stable Monitor instance ID was not rendered'
grep -Fq 'credential_file: ~/.config/moox/eventbus/monitor-observability.yaml' \
  "${MONITOR_DEPLOY}/monitor/config/app.yaml" || fail 'Monitor observability credential was not rendered'
mkdir -p "${TMP}/captures"
cat >"${MONITOR_DEPLOY}/bin/moox-gateway" <<'SH'
#!/usr/bin/env bash
env >"${MOOX_TEST_CAPTURE_DIR}/gateway.env"
sleep 30
SH
cat >"${MONITOR_DEPLOY}/bin/moox-monitor" <<'SH'
#!/usr/bin/env bash
env >"${MOOX_TEST_CAPTURE_DIR}/monitor.env"
sleep 30
SH
cat >"${MONITOR_DEPLOY}/bin/moox-eventbus" <<'SH'
#!/usr/bin/env bash
env >"${MOOX_TEST_CAPTURE_DIR}/eventbus.env"
sleep 30
SH
printf '#!/usr/bin/env bash\nexit 0\n' >"${MONITOR_DEPLOY}/bin/moox-cli"
printf '#!/usr/bin/env bash\nexit 0\n' >"${MONITOR_DEPLOY}/bin/moox-monitor-cli"
chmod +x "${MONITOR_DEPLOY}/bin/moox-gateway" "${MONITOR_DEPLOY}/bin/moox-monitor" "${MONITOR_DEPLOY}/bin/moox-eventbus" \
  "${MONITOR_DEPLOY}/bin/moox-cli" "${MONITOR_DEPLOY}/bin/moox-monitor-cli"
printf 'MOOX_MSGBOX_WECOM_WEBHOOK=https://example.invalid/webhook\n' >"${MONITOR_DEPLOY}/secrets/msgbox.env"
metadata_port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')
python3 -m http.server "${metadata_port}" --bind 127.0.0.1 >/dev/null 2>&1 &
TEST_PIDS+=("$!")
MOOX_TEST_CAPTURE_DIR="${TMP}/captures" STARTUP_WAIT_SECONDS=0 "${MONITOR_DEPLOY}/start.sh" gateway >/dev/null
TEST_PIDS+=("$(cat "${MONITOR_DEPLOY}/run/gateway.pid")")
MOOX_TEST_CAPTURE_DIR="${TMP}/captures" STARTUP_WAIT_SECONDS=0 MOOX_WAIT_EVENTBUS_SECONDS=0 \
  "${MONITOR_DEPLOY}/start.sh" eventbus >/dev/null 2>&1 || true
TEST_PIDS+=("$(cat "${MONITOR_DEPLOY}/run/eventbus.pid")")
MOOX_TEST_CAPTURE_DIR="${TMP}/captures" STARTUP_WAIT_SECONDS=0 \
  MOOX_METRICS_STORAGE_METADATA_URL="http://127.0.0.1:${metadata_port}" \
  "${MONITOR_DEPLOY}/start.sh" monitor >/dev/null
TEST_PIDS+=("$(cat "${MONITOR_DEPLOY}/run/monitor.pid")")
for _ in $(seq 1 50); do
  [[ -s "${TMP}/captures/gateway.env" && -s "${TMP}/captures/eventbus.env" && -s "${TMP}/captures/monitor.env" ]] && break
  sleep 0.1
done
grep -Fq 'MOOX_GATEWAY_SERVICE_KEY_ID=monitor' "${TMP}/captures/monitor.env" || fail 'Monitor did not receive its Gateway key ID'
if grep -Fq 'MOOX_GATEWAY_SERVICE_SECRET_KEY=service-secret' "${TMP}/captures/monitor.env"; then fail 'Monitor inherited the shared Gateway service key'; fi
grep -Fq 'MOOX_MONITOR_INSTANCE_ID=monitor-local' "${TMP}/captures/monitor.env" || fail 'Monitor did not receive its stable instance ID'
! grep -Fq 'MOOX_GATEWAY_CONTROL_SECRET_KEY=' "${TMP}/captures/monitor.env" || fail 'Monitor inherited the Gateway control key'
! grep -Fq 'MOOX_GATEWAY_SERVICE_SECRET_KEY=' "${TMP}/captures/gateway.env" || fail 'Gateway process inherited the service key instead of reading its raw key file'
! grep -Fq 'MOOX_GATEWAY_CONTROL_SECRET_KEY=' "${TMP}/captures/gateway.env" || fail 'Gateway process inherited the control key instead of reading its raw key file'
! grep -Fq 'MOOX_MONITOR_INSTANCE_ID=' "${TMP}/captures/gateway.env" || fail 'Gateway inherited the Monitor instance ID'
! grep -Fq 'MOOX_GATEWAY_SERVICE_SECRET_KEY=' "${TMP}/captures/eventbus.env" || fail 'Eventbus inherited the Gateway service key'
! grep -Fq 'MOOX_GATEWAY_CONTROL_SECRET_KEY=' "${TMP}/captures/eventbus.env" || fail 'Eventbus inherited the Gateway control key'

# A Gateway + Monitor-only node must start without an EventBus or Storage
# metadata endpoint; availability checks remain usable without metric storage.
"${DEPLOY}" --target localhost --dir "${TMP}/deploy-peer-only" --stage "${TMP}/stage-peer-only" \
  --skip-build --no-start --no-admin --no-storage --no-archive --no-eventbus \
  --no-cloudnode --no-collector --no-factor --no-strategy --no-trade --local-ca skip --target-ca skip \
  --node-id gateway-peer-only --gateway-control-url 'http://[::1]:11000' \
  --monitor-instance-id monitor-peer-only \
  --gateway-ca-bundle "${TMP}/peers.pem" --gateway-control-key-file "${TMP}/control.key" \
  --gateway-service-key-file "${TMP}/service.key" >/dev/null
PEER_ONLY_DEPLOY="${TMP}/deploy-peer-only"
grep -A1 '^metrics:$' "${PEER_ONLY_DEPLOY}/monitor/config/app.yaml" | grep -Fq 'enabled: false' || \
  fail 'Monitor-only package left metrics enabled'
cat >"${PEER_ONLY_DEPLOY}/bin/moox-monitor" <<'SH'
#!/usr/bin/env bash
sleep 30
SH
printf '#!/usr/bin/env bash\nexit 0\n' >"${PEER_ONLY_DEPLOY}/bin/moox-monitor-cli"
chmod +x "${PEER_ONLY_DEPLOY}/bin/moox-monitor" "${PEER_ONLY_DEPLOY}/bin/moox-monitor-cli"
STARTUP_WAIT_SECONDS=0 "${PEER_ONLY_DEPLOY}/start.sh" monitor >/dev/null
TEST_PIDS+=("$(cat "${PEER_ONLY_DEPLOY}/run/monitor.pid")")
mkdir -p "${PEER_ONLY_DEPLOY}/config/caddy"
printf 'MOOX_CADDY_PORTS=443\n' >"${PEER_ONLY_DEPLOY}/config/caddy/edge.env"
for mode in no-start missing-public-host; do
  extra_args=()
  [[ "${mode}" != no-start ]] || extra_args+=(--no-start)
  if output=$("${DEPLOY}" --target localhost --dir "${PEER_ONLY_DEPLOY}" --stage "${TMP}/stage-existing-caddy-${mode}" \
    --skip-build --no-admin --no-storage --no-archive --no-eventbus --no-cloudnode --no-collector --no-factor --no-strategy --no-trade \
    --local-ca skip --target-ca skip --node-id gateway-peer-only \
    --gateway-control-url 'http://[::1]:11000' --monitor-instance-id monitor-peer-only \
    --gateway-ca-bundle "${TMP}/peers.pem" --gateway-control-key-file "${TMP}/control.key" \
    --gateway-service-key-file "${TMP}/service.key" ${extra_args[@]+"${extra_args[@]}"} 2>&1); then
    fail "${mode} replaced an existing managed Caddy deployment"
  fi
  [[ "${output}" == *'existing managed Caddy'* ]] || fail "${mode} Caddy rejection was unclear"
done

expect_monitor_arg_rejected() {
  local label="$1"; shift
  local output
  if output=$("${DEPLOY}" --target localhost --dir "${TMP}/reject-${label}" --stage "${TMP}/reject-stage-${label}" \
    --skip-build --no-start --no-admin --no-storage --no-archive --no-eventbus \
    --no-cloudnode --no-collector --no-factor --no-strategy --no-trade --local-ca skip --target-ca skip \
    --node-id gateway-monitor --gateway-control-url 'http://127.0.0.1:11000' \
    --gateway-ca-bundle "${TMP}/peers.pem" --gateway-control-key-file "${TMP}/control.key" \
    --gateway-service-key-file "${TMP}/service.key" "$@" 2>&1); then
    fail "invalid Monitor arguments were accepted: ${label}"
  fi
  grep -Fq -- 'monitor' <<<"${output}" || fail "Monitor rejection was unclear: ${label}"
}
expect_monitor_arg_rejected missing-instance
expect_monitor_arg_rejected bad-instance --monitor-instance-id '../monitor'

# Remote deployment archives use collision-free 0600 paths and the EXIT trap
# removes both local and remote copies even when remote extraction fails.
mkdir -p "${TMP}/fake-bin" "${TMP}/fake-remote"
cat >"${TMP}/fake-bin/ssh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
cmd=""
for arg in "$@"; do cmd="${arg}"; done
case "${cmd}" in
  *'mktemp /tmp/moox-deploy.XXXXXX'*)
    local_path=$(mktemp "${FAKE_REMOTE_DIR}/moox-deploy.XXXXXX")
    chmod 0600 "${local_path}"
    printf '/tmp/%s\n' "$(basename "${local_path}")" | tee "${FAKE_REMOTE_DIR}/current" >/dev/stdout
    ;;
  *'chmod 0600 --'*)
    remote_path=$(cat "${FAKE_REMOTE_DIR}/current")
    chmod 0600 "${FAKE_REMOTE_DIR}/$(basename "${remote_path}")"
    ;;
  *'bash -s'*)
    cat >/dev/null
    remote_path=$(cat "${FAKE_REMOTE_DIR}/current")
    archive="${FAKE_REMOTE_DIR}/$(basename "${remote_path}")"
    if mode=$(stat -f '%Lp' "${archive}" 2>/dev/null); then
      :
    else
      mode=$(stat -c '%a' "${archive}")
    fi
    printf '%s %s\n' "${mode}" "${remote_path}" >>"${FAKE_REMOTE_DIR}/observed"
    if [[ "${FAKE_REMOTE_SUCCESS:-0}" == 1 ]]; then
      rm -f "${archive}"
      exit 0
    fi
    exit 23
    ;;
  *'rm -f --'*)
    if [[ -f "${FAKE_REMOTE_DIR}/current" ]]; then
      remote_path=$(cat "${FAKE_REMOTE_DIR}/current")
      rm -f "${FAKE_REMOTE_DIR}/$(basename "${remote_path}")"
    fi
    ;;
esac
SH
cat >"${TMP}/fake-bin/scp" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
source_path=""
destination=""
for arg in "$@"; do
  [[ "${arg}" == -* ]] && continue
  if [[ -z "${source_path}" ]]; then source_path="${arg}"; else destination="${arg}"; fi
done
remote_path=${destination#*:}
cp -p "${source_path}" "${FAKE_REMOTE_DIR}/$(basename "${remote_path}")"
printf '%s\n' "${source_path}" >>"${FAKE_REMOTE_DIR}/local-paths"
SH
chmod +x "${TMP}/fake-bin/ssh" "${TMP}/fake-bin/scp"
for attempt in 1 2; do
  if PATH="${TMP}/fake-bin:${PATH}" FAKE_REMOTE_DIR="${TMP}/fake-remote" \
    "${DEPLOY}" --target fake@example --dir /tmp/moox-test --stage "${TMP}/remote-stage-${attempt}" \
      --goos linux --goarch amd64 --skip-build --no-start --no-admin --no-storage --no-archive \
      --no-eventbus --no-cloudnode --no-collector --no-factor --no-strategy --no-trade --no-monitor --local-ca skip --target-ca skip \
      --node-id gateway-remote --gateway-control-url https://admin.example.com:9527 \
      --gateway-ca-bundle "${TMP}/peers.pem" --gateway-control-key-file "${TMP}/control.key" \
      --gateway-service-key-file "${TMP}/service.key" >/dev/null 2>&1; then
    fail 'fake remote extraction failure was unexpectedly accepted'
  fi
done
PATH="${TMP}/fake-bin:${PATH}" FAKE_REMOTE_DIR="${TMP}/fake-remote" FAKE_REMOTE_SUCCESS=1 \
  "${DEPLOY}" --target fake@example --dir /tmp/moox-test --stage "${TMP}/remote-stage-success" \
    --goos linux --goarch amd64 --skip-build --no-start --no-admin --no-storage --no-archive \
    --no-eventbus --no-cloudnode --no-collector --no-factor --no-strategy --no-trade --no-monitor --local-ca skip --target-ca skip \
    --node-id gateway-remote --gateway-control-url https://admin.example.com:9527 \
    --gateway-ca-bundle "${TMP}/peers.pem" --gateway-control-key-file "${TMP}/control.key" \
    --gateway-service-key-file "${TMP}/service.key" >/dev/null
[[ "$(wc -l <"${TMP}/fake-remote/observed" | tr -d '[:space:]')" == 3 ]] || fail 'remote archive success/failure paths were not all observed'
awk '$1 != "600" { exit 1 }' "${TMP}/fake-remote/observed" || fail 'remote deployment archive was not mode 0600'
[[ "$(awk '{print $2}' "${TMP}/fake-remote/observed" | sort -u | wc -l | tr -d '[:space:]')" == 3 ]] || fail 'remote deployment archive paths collided'
while IFS= read -r local_archive; do
  [[ ! -e "${local_archive}" ]] || fail 'local deployment archive survived failure cleanup'
done <"${TMP}/fake-remote/local-paths"
if find "${TMP}/fake-remote" -name 'moox-deploy.*' -type f -print -quit | grep -q .; then
  fail 'remote deployment archive survived failure cleanup'
fi

printf 'PASS: Gateway build, package, lifecycle, secret, CA, and Caddy contracts\n'
