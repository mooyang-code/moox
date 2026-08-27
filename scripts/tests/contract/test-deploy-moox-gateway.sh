#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
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

for option in --node-id --gateway-control-url --gateway-ca-bundle --gateway-control-key-file --gateway-service-key-file --monitor-instance-id --no-admin --component-overlay; do
  assert_contains "${DEPLOY}" "${option}"
done
assert_absent "${DEPLOY}" '--monitor-peer'
assert_contains "${DEPLOY}" 'MOOX_ADMIN_NODE_ID'
assert_contains "${DEPLOY}" 'MOOX_GATEWAY_NODE_ID'
assert_contains "${DEPLOY}" 'MOOX_RUNTIME_NODE_ID'
assert_contains "${DEPLOY}" 'MOOX_ADMIN_NODE_ID="${MOOX_ADMIN_NODE_ID:-${MOOX_RUNTIME_NODE_ID}}"'
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
assert_contains "${DEPLOY}" '[[ "${WITH_GATEWAY}" == "1" ]] && MOOX_WITH_GATEWAY=1 "${DEPLOY_DIR}/stop.sh" gateway || true'
assert_contains "${DEPLOY}" 'moox-gateway (deleted)'
assert_contains "${DEPLOY}" 'gateway_ready()'
assert_contains "${DEPLOY}" 'gateway-rollback'
assert_contains "${DEPLOY}" 'previous Gateway restored'
if ! "${DEPLOY}" --help >/dev/null; then
  fail 'deploy-moox --help failed'
fi
assert_absent "${DEPLOY}" '[[ "${WITH_EVENTBUS}" == "1" ]] || disabled_services+=(eventbus)'
assert_absent "${DEPLOY}" '[[ "${WITH_GATEWAY}" == "1" ]] || disabled_services+=(moox_gateway)'
assert_absent "${DEPLOY}" 'source "${ROOT}/secrets/gateway-control.env"'
assert_absent "${DEPLOY}" 'source "${ROOT}/secrets/gateway-service.env"'
assert_contains "${DEPLOY}" 'env "${RUNTIME_IDENTITY_ENV[@]}" "${ADMIN_SECRET_ENV[@]}"'
assert_contains "${DEPLOY}" 'gateway_service_env_for monitor'
assert_contains "${DEPLOY}" 'LOCAL_DEPLOY_ARCHIVE=$(mktemp'
assert_contains "${DEPLOY}" 'REMOTE_DEPLOY_ARCHIVE'
assert_contains "${DEPLOY}" 'cleanup_deploy_artifacts'
assert_absent "${DEPLOY}" 'rm -f "${gateway_key_path}"'
assert_absent "${DEPLOY}" 'rm -f "${credential_path}"'
assert_absent "${DEPLOY}" 'rm -f "${shared_gateway_path}"'

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
grep -Fq '"key_id":"moox-skill","caller":"moox-skill","secret_file":"gateway-moox-skill.key"' \
  "${DEPLOYED}/secrets/gateway-credentials.json" || fail 'moox-skill Gateway identity was not registered'
[[ "$(grep -o '"key_id":"moox-skill"' "${DEPLOYED}/secrets/gateway-credentials.json" | wc -l | tr -d '[:space:]')" == 1 ]] || \
  fail 'moox-skill Gateway identity was not registered exactly once'
[[ -s "${DEPLOYED}/secrets/gateway-moox-skill.key" ]] || fail 'moox-skill Gateway key was not staged'
[[ "$(file_mode "${DEPLOYED}/secrets/gateway-moox-skill.key")" == 600 ]] || fail 'moox-skill Gateway key mode is not 600'
! cmp -s "${TMP}/service.key" "${DEPLOYED}/secrets/gateway-moox-skill.key" || fail 'moox-skill Gateway key reused the root service secret'
cmp -s "${TMP}/peers.pem" "${DEPLOYED}/certs/gateway/peers.pem" || fail 'public peer CA was not installed'
! grep -Rqs -- 'PRIVATE KEY' "${DEPLOYED}/certs" || fail 'a private CA key was staged'
for secret in gateway-control.env gateway-service.env gateway-control.key gateway-service.key; do
  mode=$(file_mode "${DEPLOYED}/secrets/${secret}")
  [[ "${mode}" == 600 ]] || fail "${secret} mode is ${mode}, want 600"
done

# Component upgrades must replace a stale registry that predates moox-skill.
printf 'local-symlink-sentinel\n' >"${TMP}/local-key-sentinel"
rm -f "${DEPLOYED}/secrets/gateway-factor.key"
ln -s "${TMP}/local-key-sentinel" "${DEPLOYED}/secrets/gateway-factor.key"
printf ' \n' >"${DEPLOYED}/secrets/gateway-monitor.key"
printf 'touch %q\n' "${TMP}/local-env-executed" >"${TMP}/local-env-sentinel"
chmod 0644 "${TMP}/local-env-sentinel"
rm -f "${DEPLOYED}/secrets/gateway-control.env"
ln -s "${TMP}/local-env-sentinel" "${DEPLOYED}/secrets/gateway-control.env"
printf ' \n' >"${DEPLOYED}/secrets/gateway-service.env"
printf 'touch %q\n' "${TMP}/local-cli-env-executed" >"${TMP}/local-cli-env-sentinel"
chmod 0644 "${TMP}/local-cli-env-sentinel"
rm -f "${DEPLOYED}/secrets/gateway-moox-cli.env"
ln -s "${TMP}/local-cli-env-sentinel" "${DEPLOYED}/secrets/gateway-moox-cli.env"
printf '%s\n' '{"version":1,"credentials":[{"key_id":"moox-gateway-service","caller":"admin-gateway","secret_file":"gateway-service.key"}]}' \
  >"${DEPLOYED}/secrets/gateway-credentials.json"
MOOX_STORAGE_PRIMARY_AUTH_SECRET=gateway-contract-primary \
MOOX_STORAGE_VIEW_AUTH_SECRET=gateway-contract-view \
  "${DEPLOY}" --target localhost --dir "${DEPLOYED}" --stage "${TMP}/stage-registry-upgrade" \
  --skip-build --no-start --component-overlay --no-admin --no-storage --no-archive --no-eventbus \
  --no-cloudnode --no-collector --no-factor --no-strategy --no-trade --no-monitor --local-ca skip --target-ca skip \
  --node-id gateway-test --gateway-control-url 'https://admin.example.com:9527/' \
  --gateway-ca-bundle "${TMP}/peers.pem" --gateway-control-key-file "${TMP}/control.key" \
  --gateway-service-key-file "${TMP}/service.key" >/dev/null
grep -Fq '"key_id":"moox-skill","caller":"moox-skill","secret_file":"gateway-moox-skill.key"' \
  "${DEPLOYED}/secrets/gateway-credentials.json" || fail 'component upgrade preserved a stale Gateway registry'
[[ ! -L "${DEPLOYED}/secrets/gateway-factor.key" && -s "${DEPLOYED}/secrets/gateway-factor.key" ]] || \
  fail 'local component overlay preserved an unsafe Gateway key path'
[[ "$(file_mode "${DEPLOYED}/secrets/gateway-factor.key")" == 600 ]] || \
  fail 'local component overlay replaced an unsafe Gateway key with the wrong mode'
grep -Fxq 'local-symlink-sentinel' "${TMP}/local-key-sentinel" || \
  fail 'local component overlay followed an unsafe Gateway key symlink'
grep -q '[^[:space:]]' "${DEPLOYED}/secrets/gateway-monitor.key" || \
  fail 'local component overlay preserved a whitespace-only Gateway key'
[[ ! -L "${DEPLOYED}/secrets/gateway-control.env" && -s "${DEPLOYED}/secrets/gateway-control.env" ]] || \
  fail 'local component overlay preserved an unsafe Gateway env path'
[[ "$(file_mode "${TMP}/local-env-sentinel")" == 644 && ! -e "${TMP}/local-env-executed" ]] || \
  fail 'local component overlay followed, chmodded, or sourced an unsafe Gateway env symlink'
grep -q '[^[:space:]]' "${DEPLOYED}/secrets/gateway-service.env" || \
  fail 'local component overlay preserved a whitespace-only Gateway env'
[[ ! -L "${DEPLOYED}/secrets/gateway-moox-cli.env" ]] || \
  fail 'local component overlay preserved an unsafe Gateway CLI env path'
grep -Fxq "MOOX_GATEWAY_SERVICE_SECRET_KEY=$(tr -d '\r\n' <"${DEPLOYED}/secrets/gateway-moox-cli.key")" \
  "${DEPLOYED}/secrets/gateway-moox-cli.env" || fail 'local Gateway CLI env does not match its key'
grep -Fxq "MOOX_COLLECTOR_GATEWAY_SERVICE_SECRET_KEY=$(tr -d '\r\n' <"${DEPLOYED}/secrets/gateway-collector.key")" \
  "${DEPLOYED}/secrets/gateway-moox-cli.env" || fail 'local Gateway CLI env does not match the collector key'
[[ "$(file_mode "${TMP}/local-cli-env-sentinel")" == 644 && ! -e "${TMP}/local-cli-env-executed" ]] || \
  fail 'local component overlay followed, chmodded, or sourced an unsafe Gateway CLI env symlink'

LOCAL_REGISTRY_FAILURE="${TMP}/local-registry-failure"
cp -R "${DEPLOYED}" "${LOCAL_REGISTRY_FAILURE}"
printf 'local-registry-failure-key\n' >"${TMP}/local-registry-failure-key"
rm -f "${LOCAL_REGISTRY_FAILURE}/secrets/gateway-factor.key"
ln -s "${TMP}/local-registry-failure-key" "${LOCAL_REGISTRY_FAILURE}/secrets/gateway-factor.key"
rm -f "${LOCAL_REGISTRY_FAILURE}/secrets/gateway-credentials.json"
mkdir "${LOCAL_REGISTRY_FAILURE}/secrets/gateway-credentials.json"
if MOOX_STORAGE_PRIMARY_AUTH_SECRET=gateway-contract-primary \
  MOOX_STORAGE_VIEW_AUTH_SECRET=gateway-contract-view \
  "${DEPLOY}" --target localhost --dir "${LOCAL_REGISTRY_FAILURE}" --stage "${TMP}/stage-registry-failure" \
  --skip-build --no-start --component-overlay --no-admin --no-storage --no-archive --no-eventbus \
  --no-cloudnode --no-collector --no-factor --no-strategy --no-trade --no-monitor --local-ca skip --target-ca skip \
  --node-id gateway-test --gateway-control-url 'https://admin.example.com:9527/' \
  --gateway-ca-bundle "${TMP}/peers.pem" --gateway-control-key-file "${TMP}/control.key" \
  --gateway-service-key-file "${TMP}/service.key" >/dev/null 2>&1; then
  fail 'local component overlay accepted an unsafe Gateway registry target'
fi
[[ -L "${LOCAL_REGISTRY_FAILURE}/secrets/gateway-factor.key" ]] && \
  grep -Fxq 'local-registry-failure-key' "${TMP}/local-registry-failure-key" || \
  fail 'local registry failure partially replaced Gateway secrets'

LOCAL_CLS_UNSAFE="${TMP}/local-cls-unsafe"
cp -R "${DEPLOYED}" "${LOCAL_CLS_UNSAFE}"
printf 'touch %q\n' "${TMP}/local-cls-env-executed" >"${TMP}/local-cls-env-sentinel"
rm -f "${LOCAL_CLS_UNSAFE}/secrets/gateway-moox-cli.env"
ln -s "${TMP}/local-cls-env-sentinel" "${LOCAL_CLS_UNSAFE}/secrets/gateway-moox-cli.env"
if MOOX_STORAGE_PRIMARY_AUTH_SECRET=gateway-contract-primary \
  MOOX_STORAGE_VIEW_AUTH_SECRET=gateway-contract-view \
  "${DEPLOY}" --target localhost --dir "${LOCAL_CLS_UNSAFE}" --stage "${TMP}/stage-cls-unsafe" \
  --skip-build --no-start --enable-cls --component-overlay --no-gateway --no-admin --no-storage --no-archive --no-eventbus \
  --no-cloudnode --no-collector --no-factor --no-strategy --no-trade --no-monitor --local-ca skip --target-ca skip \
  --node-id gateway-test --gateway-control-url 'https://admin.example.com:9527/' \
  --gateway-ca-bundle "${TMP}/peers.pem" --gateway-control-key-file "${TMP}/control.key" \
  --gateway-service-key-file "${TMP}/service.key" >/dev/null 2>&1; then
  fail 'CLS preflight accepted an unsafe Gateway CLI env'
fi
[[ ! -e "${TMP}/local-cls-env-executed" ]] || \
  fail 'CLS preflight sourced an unsafe Gateway CLI env before validation'

LOCAL_CLS_WRITABLE="${TMP}/local-cls-writable"
cp -R "${DEPLOYED}" "${LOCAL_CLS_WRITABLE}"
printf 'LD_PRELOAD=/tmp/unsafe.so\n' >>"${LOCAL_CLS_WRITABLE}/secrets/gateway-moox-cli.env"
chmod 0666 "${LOCAL_CLS_WRITABLE}/secrets/gateway-moox-cli.env"
if cls_output=$(MOOX_STORAGE_PRIMARY_AUTH_SECRET=gateway-contract-primary \
  MOOX_STORAGE_VIEW_AUTH_SECRET=gateway-contract-view \
  "${DEPLOY}" --target localhost --dir "${LOCAL_CLS_WRITABLE}" --stage "${TMP}/stage-cls-writable" \
  --skip-build --no-start --enable-cls --component-overlay --no-gateway --no-admin --no-storage --no-archive --no-eventbus \
  --no-cloudnode --no-collector --no-factor --no-strategy --no-trade --no-monitor --local-ca skip --target-ca skip \
  --node-id gateway-test --gateway-control-url 'https://admin.example.com:9527/' \
  --gateway-ca-bundle "${TMP}/peers.pem" --gateway-control-key-file "${TMP}/control.key" \
  --gateway-service-key-file "${TMP}/service.key" 2>&1); then
  fail 'CLS preflight accepted a writable Gateway CLI env with an extra variable'
fi
grep -Fq 'CLS preflight refuses unsafe gateway-moox-cli.env' <<<"${cls_output}" || \
  fail 'writable Gateway CLI env was not rejected by the early safety guard'

NO_GATEWAY_DEPLOY="${TMP}/deploy-no-gateway-overlay"
cp -R "${DEPLOYED}" "${NO_GATEWAY_DEPLOY}"
printf '%s\n' '{"version":1,"credentials":[{"key_id":"collector","caller":"collector","secret_file":"gateway-collector.key"}]}' \
  >"${NO_GATEWAY_DEPLOY}/secrets/gateway-credentials.json"
printf 'preserve-no-gateway-skill\n' >"${NO_GATEWAY_DEPLOY}/secrets/gateway-moox-skill.key"
MOOX_STORAGE_PRIMARY_AUTH_SECRET=gateway-contract-primary \
MOOX_STORAGE_VIEW_AUTH_SECRET=gateway-contract-view \
  "${DEPLOY}" --target localhost --dir "${NO_GATEWAY_DEPLOY}" --stage "${TMP}/stage-no-gateway-overlay" \
  --skip-build --no-start --component-overlay --no-gateway --no-admin --no-storage --no-archive --no-eventbus \
  --no-cloudnode --no-collector --no-factor --no-strategy --no-trade --local-ca skip --target-ca skip \
  --node-id gateway-test --gateway-control-url 'https://admin.example.com:9527/' --monitor-instance-id monitor-local \
  --gateway-ca-bundle "${TMP}/peers.pem" --gateway-control-key-file "${TMP}/control.key" \
  --gateway-service-key-file "${TMP}/service.key" >/dev/null
! grep -Fq '"key_id":"moox-skill"' "${NO_GATEWAY_DEPLOY}/secrets/gateway-credentials.json" || \
  fail 'local --no-gateway overlay changed the active Gateway registry'
grep -Fxq 'preserve-no-gateway-skill' "${NO_GATEWAY_DEPLOY}/secrets/gateway-moox-skill.key" || \
  fail 'local --no-gateway overlay rotated the inactive skill key'
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
MOOX_EVENTBUS_ENABLE_TLS=1 \
MOOX_STORAGE_PRIMARY_AUTH_SECRET=gateway-contract-primary \
MOOX_STORAGE_VIEW_AUTH_SECRET=gateway-contract-view \
  "${DEPLOY}" --target localhost --dir "${TMP}/deploy-monitor" --stage "${TMP}/stage-monitor" \
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
grep -Fq 'MOOX_OBSERVABILITY_CREDENTIAL_FILE=${MONITOR_OBSERVABILITY_CREDENTIAL_FILE}' \
  "${MONITOR_DEPLOY}/start.sh" || fail 'Monitor lifecycle did not pass observability credentials'
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
printf 'MOOX_NOTIFICATION_WEBHOOK_URL=https://example.invalid/webhook\n' >"${MONITOR_DEPLOY}/secrets/notification.env"
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
MOOX_STORAGE_PRIMARY_AUTH_SECRET=gateway-contract-primary \
MOOX_STORAGE_VIEW_AUTH_SECRET=gateway-contract-view \
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
# A workload-only overlay does not own Caddy, so a no-start package update may
# run without repeating the public host and must preserve the edge state.
	if ! output=$(MOOX_STORAGE_PRIMARY_AUTH_SECRET=gateway-contract-primary \
	  MOOX_STORAGE_VIEW_AUTH_SECRET=gateway-contract-view \
	  "${DEPLOY}" --target localhost --dir "${PEER_ONLY_DEPLOY}" --stage "${TMP}/stage-existing-caddy-overlay" \
  --skip-build --no-start --component-overlay --no-admin --no-storage --no-archive --no-eventbus --no-cloudnode --no-collector --no-factor --no-strategy --no-trade \
  --local-ca skip --target-ca skip --node-id gateway-peer-only \
  --gateway-control-url 'http://[::1]:11000' --monitor-instance-id monitor-peer-only \
  --gateway-ca-bundle "${TMP}/peers.pem" --gateway-control-key-file "${TMP}/control.key" \
  --gateway-service-key-file "${TMP}/service.key" 2>&1); then
  fail "no-start workload overlay failed: ${output}"
fi
grep -Fxq 'MOOX_CADDY_PORTS=443' "${PEER_ONLY_DEPLOY}/config/caddy/edge.env" || \
  fail 'workload overlay replaced existing Caddy state'

expect_monitor_arg_rejected() {
  local label="$1"; shift
  local output
  if output=$(MOOX_STORAGE_PRIMARY_AUTH_SECRET=gateway-contract-primary \
    MOOX_STORAGE_VIEW_AUTH_SECRET=gateway-contract-view \
    "${DEPLOY}" --target localhost --dir "${TMP}/reject-${label}" --stage "${TMP}/reject-stage-${label}" \
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

# Exercise the embedded remote extraction script directly. A component overlay
# must preserve established caller keys while filling missing registry keys and
# replacing a registry that predates the skill identity.
REMOTE_OVERLAY_ROOT="${TMP}/remote-overlay-root"
REMOTE_OVERLAY_PAYLOAD="${TMP}/remote-overlay-payload"
REMOTE_OVERLAY_ARCHIVE="${TMP}/remote-overlay.tar.gz"
REMOTE_NO_GATEWAY_ARCHIVE="${TMP}/remote-no-gateway-overlay.tar.gz"
REMOTE_FAILURE_ARCHIVE="${TMP}/remote-failure-overlay.tar.gz"
REMOTE_OVERLAY_SCRIPT="${TMP}/remote-overlay.sh"
REMOTE_NO_GATEWAY_ROOT="${TMP}/remote-no-gateway-root"
REMOTE_NO_GATEWAY_UNSAFE_ROOT="${TMP}/remote-no-gateway-unsafe-root"
REMOTE_FAILURE_ROOT="${TMP}/remote-failure-root"
mkdir -p "${REMOTE_OVERLAY_ROOT}/bin" "${REMOTE_OVERLAY_ROOT}/config" "${REMOTE_OVERLAY_ROOT}/secrets" \
  "${REMOTE_OVERLAY_PAYLOAD}/bin" "${REMOTE_OVERLAY_PAYLOAD}/monitor" "${REMOTE_OVERLAY_PAYLOAD}/secrets"
printf '#!/usr/bin/env bash\n# MOOX_INSTALLED_WITH_ inventory marker\nexit 0\n' >"${REMOTE_OVERLAY_ROOT}/start.sh"
for script in stop.sh status.sh healthcheck.sh; do
  printf '#!/usr/bin/env bash\nexit 0\n' >"${REMOTE_OVERLAY_ROOT}/${script}"
done
chmod +x "${REMOTE_OVERLAY_ROOT}/start.sh" "${REMOTE_OVERLAY_ROOT}/stop.sh" \
  "${REMOTE_OVERLAY_ROOT}/status.sh" "${REMOTE_OVERLAY_ROOT}/healthcheck.sh"
printf 'MOOX_INSTALLED_WITH_ADMIN=1\n' >"${REMOTE_OVERLAY_ROOT}/config/components.env"
printf 'MOOX_HEALTH_AUTH_VERSION=v1\nMOOX_HEALTH_AUTH_ACCESS_KEY=test\nMOOX_HEALTH_AUTH_SECRET_KEY=test\n' \
  >"${REMOTE_OVERLAY_ROOT}/secrets/health-auth.env"
for secret in gateway-control.env gateway-service.env gateway-control.key gateway-service.key; do
  printf 'shared-%s\n' "${secret}" >"${REMOTE_OVERLAY_ROOT}/secrets/${secret}"
done
printf 'preserve-collector-key\n' >"${REMOTE_OVERLAY_ROOT}/secrets/gateway-collector.key"
printf ' \n' >"${REMOTE_OVERLAY_ROOT}/secrets/gateway-factor.key"
printf 'preserve-skill-key\n' >"${REMOTE_OVERLAY_ROOT}/secrets/gateway-moox-skill.key"
REMOTE_CLI_SECRET="abc\\nPWN=\$(touch ${TMP}/remote-cli-key-executed)"
printf '%s\n' "${REMOTE_CLI_SECRET}" >"${REMOTE_OVERLAY_ROOT}/secrets/gateway-moox-cli.key"
printf 'remote-symlink-sentinel\n' >"${TMP}/remote-key-sentinel"
ln -s "${TMP}/remote-key-sentinel" "${REMOTE_OVERLAY_ROOT}/secrets/gateway-storage-view.key"
printf 'touch %q\n' "${TMP}/remote-env-executed" >"${TMP}/remote-env-sentinel"
chmod 0644 "${TMP}/remote-env-sentinel"
rm -f "${REMOTE_OVERLAY_ROOT}/secrets/gateway-service.env"
ln -s "${TMP}/remote-env-sentinel" "${REMOTE_OVERLAY_ROOT}/secrets/gateway-service.env"
printf ' \n' >"${REMOTE_OVERLAY_ROOT}/secrets/gateway-control.env"
printf 'touch %q\n' "${TMP}/remote-cli-env-executed" >"${TMP}/remote-cli-env-sentinel"
chmod 0644 "${TMP}/remote-cli-env-sentinel"
ln -s "${TMP}/remote-cli-env-sentinel" "${REMOTE_OVERLAY_ROOT}/secrets/gateway-moox-cli.env"
printf '%s\n' '{"version":1,"credentials":[{"key_id":"collector","caller":"collector","secret_file":"gateway-collector.key"}]}' \
  >"${REMOTE_OVERLAY_ROOT}/secrets/gateway-credentials.json"
cp -R "${REMOTE_OVERLAY_ROOT}" "${REMOTE_NO_GATEWAY_ROOT}"
cp -R "${REMOTE_OVERLAY_ROOT}" "${REMOTE_NO_GATEWAY_UNSAFE_ROOT}"
cp -R "${REMOTE_OVERLAY_ROOT}" "${REMOTE_FAILURE_ROOT}"
rm -f "${REMOTE_NO_GATEWAY_ROOT}/secrets/gateway-service.env"
printf 'preserve-no-gateway-service-env\n' >"${REMOTE_NO_GATEWAY_ROOT}/secrets/gateway-service.env"
printf 'preserve-no-gateway-control-env\n' >"${REMOTE_NO_GATEWAY_ROOT}/secrets/gateway-control.env"
rm -f "${REMOTE_NO_GATEWAY_ROOT}/secrets/gateway-moox-cli.env"
printf 'preserve-no-gateway-cli-env\n' >"${REMOTE_NO_GATEWAY_ROOT}/secrets/gateway-moox-cli.env"
rm -f "${REMOTE_FAILURE_ROOT}/secrets/gateway-credentials.json"
mkdir "${REMOTE_FAILURE_ROOT}/secrets/gateway-credentials.json"

for script in start.sh stop.sh status.sh healthcheck.sh; do
  printf '#!/usr/bin/env bash\nexit 0\n' >"${REMOTE_OVERLAY_PAYLOAD}/${script}"
done
printf '#!/usr/bin/env bash\nexit 0\n' >"${REMOTE_OVERLAY_PAYLOAD}/bin/moox-monitor"
printf 'monitor payload\n' >"${REMOTE_OVERLAY_PAYLOAD}/monitor/app.yaml"
printf 'rotated-collector-key\n' >"${REMOTE_OVERLAY_PAYLOAD}/secrets/gateway-collector.key"
printf 'derived-factor-key\n' >"${REMOTE_OVERLAY_PAYLOAD}/secrets/gateway-factor.key"
printf 'derived-storage-view-key\n' >"${REMOTE_OVERLAY_PAYLOAD}/secrets/gateway-storage-view.key"
printf 'rotated-skill-key\n' >"${REMOTE_OVERLAY_PAYLOAD}/secrets/gateway-moox-skill.key"
printf 'derived-cli-key\n' >"${REMOTE_OVERLAY_PAYLOAD}/secrets/gateway-moox-cli.key"
printf 'derived-control-env\n' >"${REMOTE_OVERLAY_PAYLOAD}/secrets/gateway-control.env"
printf 'derived-service-env\n' >"${REMOTE_OVERLAY_PAYLOAD}/secrets/gateway-service.env"
printf 'MOOX_GATEWAY_SERVICE_SECRET_KEY=rotated-cli\nMOOX_COLLECTOR_GATEWAY_SERVICE_SECRET_KEY=rotated-collector\n' \
  >"${REMOTE_OVERLAY_PAYLOAD}/secrets/gateway-moox-cli.env"
printf '%s\n' '{"version":1,"credentials":[{"key_id":"collector","caller":"collector","secret_file":"gateway-collector.key"},{"key_id":"factor","caller":"factor","secret_file":"gateway-factor.key"},{"key_id":"moox-skill","caller":"moox-skill","secret_file":"gateway-moox-skill.key"}]}' \
  >"${REMOTE_OVERLAY_PAYLOAD}/secrets/gateway-credentials.json"
printf ' \n' >"${REMOTE_OVERLAY_PAYLOAD}/secrets/gateway-monitor.key"
tar -C "${REMOTE_OVERLAY_PAYLOAD}" -czf "${REMOTE_FAILURE_ARCHIVE}" .
printf 'preserve-monitor-key\n' >"${TMP}/remote-failure-key-sentinel"
rm -f "${REMOTE_FAILURE_ROOT}/secrets/gateway-monitor.key"
ln -s "${TMP}/remote-failure-key-sentinel" "${REMOTE_FAILURE_ROOT}/secrets/gateway-monitor.key"
printf 'derived-monitor-key\n' >"${REMOTE_OVERLAY_PAYLOAD}/secrets/gateway-monitor.key"
tar -C "${REMOTE_OVERLAY_PAYLOAD}" -czf "${REMOTE_OVERLAY_ARCHIVE}" .
cp "${REMOTE_OVERLAY_ARCHIVE}" "${REMOTE_NO_GATEWAY_ARCHIVE}"
awk '/bash -s" <<'"'"'EOF'"'"'$/ { capture=1; next } capture && /^EOF$/ { exit } capture { print }' \
  "${DEPLOY}" >"${REMOTE_OVERLAY_SCRIPT}"
if DEPLOY_DIR="${REMOTE_FAILURE_ROOT}" ARCHIVE="${REMOTE_FAILURE_ARCHIVE}" NODE_ID=gateway-remote-overlay \
  NO_START=1 COMPONENT_OVERLAY=1 WITH_STORAGE=0 WITH_STORAGE_NODE=0 WITH_ARCHIVE=0 WITH_EVENTBUS=0 \
  WITH_CLOUDNODE=0 WITH_COLLECTOR=0 WITH_FACTOR=0 WITH_STRATEGY=0 WITH_TRADE=0 WITH_MONITOR=1 \
  WITH_HOSTAGENT=0 WITH_WEB_HOST=0 WITH_ADMIN=0 WITH_GATEWAY=1 RESET_DATA=0 \
  MOOX_METRICS_STORAGE_METADATA_URL='' MOOX_EVENTBUS_NATS_URL='' MOOX_EVENTBUS_HOST='' MOOX_EVENTBUS_PORT='' \
  MOOX_METRICS_EVENTBUS_URL='' MOOX_EVENTBUS_ENABLE_TLS=0 MOOX_EVENTBUS_PUBLIC_IP='' \
  MOOX_LOCAL_STORAGE_RPC_GATEWAY_TARGET='' MOOX_LOCAL_STORAGE_GATEWAY_NODE_ID='' \
  MOOX_STORAGE_VIEW_DUCKDB_MEMORY_LIMIT='' MOOX_STORAGE_VIEW_MAINTENANCE_POLICY_B64='' \
  MOOX_FACTOR_ENGINE_PYTHON_WORKERS='' MOOX_FACTOR_ENGINE_VIEW_READ_WORKERS='' MOOX_FACTOR_ENGINE_VIEW_READ_TIMEOUT_MS='' \
  MOOX_CONTROL_ROOT='' MOOX_STORAGE_ROOT='' PUBLIC_HOST='' TLS_MODE_RESOLVED='' BROWSER_HTTPS_PORT=9527 \
  SERVICE_HTTPS_PORT=11001 TARGET_GOOS=linux TARGET_GOARCH=amd64 bash "${REMOTE_OVERLAY_SCRIPT}" >/dev/null 2>&1; then
  fail 'remote component overlay accepted a whitespace-only replacement key'
fi
[[ -L "${REMOTE_FAILURE_ROOT}/secrets/gateway-monitor.key" ]] && \
  grep -Fxq 'preserve-monitor-key' "${TMP}/remote-failure-key-sentinel" || \
  fail 'failed remote component overlay removed the previous abnormal key target'
[[ -L "${REMOTE_FAILURE_ROOT}/secrets/gateway-storage-view.key" ]] || \
  fail 'failed remote component overlay partially replaced an earlier Gateway key'
[[ -L "${REMOTE_FAILURE_ROOT}/secrets/gateway-service.env" ]] || \
  fail 'failed remote component overlay partially replaced an earlier Gateway env'
! grep -q '[^[:space:]]' "${REMOTE_FAILURE_ROOT}/secrets/gateway-factor.key" || \
  fail 'failed remote component overlay partially replaced a whitespace-only Gateway key'
[[ -d "${REMOTE_FAILURE_ROOT}/secrets/gateway-credentials.json" ]] || \
  fail 'failed remote component overlay changed the unsafe Gateway registry target'
DEPLOY_DIR="${REMOTE_OVERLAY_ROOT}" ARCHIVE="${REMOTE_OVERLAY_ARCHIVE}" NODE_ID=gateway-remote-overlay \
  NO_START=1 COMPONENT_OVERLAY=1 WITH_STORAGE=0 WITH_STORAGE_NODE=0 WITH_ARCHIVE=0 WITH_EVENTBUS=0 \
  WITH_CLOUDNODE=0 WITH_COLLECTOR=0 WITH_FACTOR=0 WITH_STRATEGY=0 WITH_TRADE=0 WITH_MONITOR=1 \
  WITH_HOSTAGENT=0 WITH_WEB_HOST=0 WITH_ADMIN=0 WITH_GATEWAY=1 RESET_DATA=0 \
  MOOX_METRICS_STORAGE_METADATA_URL='' MOOX_EVENTBUS_NATS_URL='' MOOX_EVENTBUS_HOST='' MOOX_EVENTBUS_PORT='' \
  MOOX_METRICS_EVENTBUS_URL='' MOOX_EVENTBUS_ENABLE_TLS=0 MOOX_EVENTBUS_PUBLIC_IP='' \
  MOOX_LOCAL_STORAGE_RPC_GATEWAY_TARGET='' MOOX_LOCAL_STORAGE_GATEWAY_NODE_ID='' \
  MOOX_STORAGE_VIEW_DUCKDB_MEMORY_LIMIT='' MOOX_STORAGE_VIEW_MAINTENANCE_POLICY_B64='' \
  MOOX_FACTOR_ENGINE_PYTHON_WORKERS='' MOOX_FACTOR_ENGINE_VIEW_READ_WORKERS='' MOOX_FACTOR_ENGINE_VIEW_READ_TIMEOUT_MS='' \
  MOOX_CONTROL_ROOT='' MOOX_STORAGE_ROOT='' PUBLIC_HOST='' TLS_MODE_RESOLVED='' BROWSER_HTTPS_PORT=9527 \
  SERVICE_HTTPS_PORT=11001 TARGET_GOOS=linux TARGET_GOARCH=amd64 bash "${REMOTE_OVERLAY_SCRIPT}" >/dev/null
grep -Fq '"key_id":"moox-skill"' "${REMOTE_OVERLAY_ROOT}/secrets/gateway-credentials.json" || \
  fail 'remote component overlay preserved the stale Gateway registry'
grep -Fxq 'preserve-skill-key' "${REMOTE_OVERLAY_ROOT}/secrets/gateway-moox-skill.key" || \
  fail 'remote component overlay rotated an established skill key'
[[ "$(file_mode "${REMOTE_OVERLAY_ROOT}/secrets/gateway-moox-skill.key")" == 600 ]] || \
  fail 'remote component overlay installed the skill key with the wrong mode'
grep -Fxq 'preserve-collector-key' "${REMOTE_OVERLAY_ROOT}/secrets/gateway-collector.key" || \
  fail 'remote component overlay replaced an established caller key'
grep -Fxq 'derived-factor-key' "${REMOTE_OVERLAY_ROOT}/secrets/gateway-factor.key" || \
  fail 'remote component overlay did not install a missing registry key'
[[ "$(file_mode "${REMOTE_OVERLAY_ROOT}/secrets/gateway-factor.key")" == 600 ]] || \
  fail 'remote component overlay installed a missing registry key with the wrong mode'
[[ ! -L "${REMOTE_OVERLAY_ROOT}/secrets/gateway-storage-view.key" ]] || \
  fail 'remote component overlay preserved an unsafe Gateway key symlink'
grep -Fxq 'derived-storage-view-key' "${REMOTE_OVERLAY_ROOT}/secrets/gateway-storage-view.key" || \
  fail 'remote component overlay did not safely replace an unsafe Gateway key path'
[[ "$(file_mode "${REMOTE_OVERLAY_ROOT}/secrets/gateway-storage-view.key")" == 600 ]] || \
  fail 'remote component overlay replaced an unsafe Gateway key with the wrong mode'
grep -Fxq 'remote-symlink-sentinel' "${TMP}/remote-key-sentinel" || \
  fail 'remote component overlay followed an unsafe Gateway key symlink'
grep -Fxq 'derived-service-env' "${REMOTE_OVERLAY_ROOT}/secrets/gateway-service.env" || \
  fail 'remote component overlay did not safely replace an unsafe Gateway env path'
grep -Fxq 'derived-control-env' "${REMOTE_OVERLAY_ROOT}/secrets/gateway-control.env" || \
  fail 'remote component overlay preserved a whitespace-only Gateway env'
[[ "$(file_mode "${TMP}/remote-env-sentinel")" == 644 && ! -e "${TMP}/remote-env-executed" ]] || \
  fail 'remote component overlay followed, chmodded, or sourced an unsafe Gateway env symlink'
[[ ! -L "${REMOTE_OVERLAY_ROOT}/secrets/gateway-moox-cli.env" ]] || \
  fail 'remote component overlay preserved an unsafe Gateway CLI env path'
grep -Fxq 'MOOX_COLLECTOR_GATEWAY_SERVICE_SECRET_KEY=preserve-collector-key' \
  "${REMOTE_OVERLAY_ROOT}/secrets/gateway-moox-cli.env" || fail 'remote Gateway CLI env does not match the preserved collector key'
unset MOOX_GATEWAY_SERVICE_SECRET_KEY
source "${REMOTE_OVERLAY_ROOT}/secrets/gateway-moox-cli.env"
[[ "${MOOX_GATEWAY_SERVICE_SECRET_KEY}" == "${REMOTE_CLI_SECRET}" && ! -e "${TMP}/remote-cli-key-executed" ]] || \
  fail 'remote Gateway CLI env did not shell-quote the preserved key safely'
[[ "$(file_mode "${TMP}/remote-cli-env-sentinel")" == 644 && ! -e "${TMP}/remote-cli-env-executed" ]] || \
  fail 'remote component overlay followed, chmodded, or sourced an unsafe Gateway CLI env symlink'

if DEPLOY_DIR="${REMOTE_NO_GATEWAY_UNSAFE_ROOT}" ARCHIVE="${REMOTE_NO_GATEWAY_ARCHIVE}" NODE_ID=gateway-remote-overlay \
  NO_START=0 COMPONENT_OVERLAY=1 WITH_STORAGE=0 WITH_STORAGE_NODE=0 WITH_ARCHIVE=0 WITH_EVENTBUS=0 \
  WITH_CLOUDNODE=0 WITH_COLLECTOR=0 WITH_FACTOR=0 WITH_STRATEGY=0 WITH_TRADE=0 WITH_MONITOR=1 \
  WITH_HOSTAGENT=0 WITH_WEB_HOST=0 WITH_ADMIN=0 WITH_GATEWAY=0 RESET_DATA=0 \
  MOOX_METRICS_STORAGE_METADATA_URL='' MOOX_EVENTBUS_NATS_URL='' MOOX_EVENTBUS_HOST='' MOOX_EVENTBUS_PORT='' \
  MOOX_METRICS_EVENTBUS_URL='' MOOX_EVENTBUS_ENABLE_TLS=0 MOOX_EVENTBUS_PUBLIC_IP='' \
  MOOX_LOCAL_STORAGE_RPC_GATEWAY_TARGET='' MOOX_LOCAL_STORAGE_GATEWAY_NODE_ID='' \
  MOOX_STORAGE_VIEW_DUCKDB_MEMORY_LIMIT='' MOOX_STORAGE_VIEW_MAINTENANCE_POLICY_B64='' \
  MOOX_FACTOR_ENGINE_PYTHON_WORKERS='' MOOX_FACTOR_ENGINE_VIEW_READ_WORKERS='' MOOX_FACTOR_ENGINE_VIEW_READ_TIMEOUT_MS='' \
  MOOX_CONTROL_ROOT='' MOOX_STORAGE_ROOT='' PUBLIC_HOST='' TLS_MODE_RESOLVED='' BROWSER_HTTPS_PORT=9527 \
  SERVICE_HTTPS_PORT=11001 TARGET_GOOS=linux TARGET_GOARCH=amd64 bash "${REMOTE_OVERLAY_SCRIPT}" >/dev/null 2>&1; then
  fail 'remote --no-gateway overlay accepted an unsafe Gateway env'
fi
[[ "$(file_mode "${TMP}/remote-env-sentinel")" == 644 && ! -e "${TMP}/remote-env-executed" ]] || \
  fail 'remote --no-gateway overlay chmodded or sourced an unsafe Gateway env symlink'

DEPLOY_DIR="${REMOTE_NO_GATEWAY_ROOT}" ARCHIVE="${REMOTE_NO_GATEWAY_ARCHIVE}" NODE_ID=gateway-remote-overlay \
  NO_START=1 COMPONENT_OVERLAY=1 WITH_STORAGE=0 WITH_STORAGE_NODE=0 WITH_ARCHIVE=0 WITH_EVENTBUS=0 \
  WITH_CLOUDNODE=0 WITH_COLLECTOR=0 WITH_FACTOR=0 WITH_STRATEGY=0 WITH_TRADE=0 WITH_MONITOR=1 \
  WITH_HOSTAGENT=0 WITH_WEB_HOST=0 WITH_ADMIN=0 WITH_GATEWAY=0 RESET_DATA=0 \
  MOOX_METRICS_STORAGE_METADATA_URL='' MOOX_EVENTBUS_NATS_URL='' MOOX_EVENTBUS_HOST='' MOOX_EVENTBUS_PORT='' \
  MOOX_METRICS_EVENTBUS_URL='' MOOX_EVENTBUS_ENABLE_TLS=0 MOOX_EVENTBUS_PUBLIC_IP='' \
  MOOX_LOCAL_STORAGE_RPC_GATEWAY_TARGET='' MOOX_LOCAL_STORAGE_GATEWAY_NODE_ID='' \
  MOOX_STORAGE_VIEW_DUCKDB_MEMORY_LIMIT='' MOOX_STORAGE_VIEW_MAINTENANCE_POLICY_B64='' \
  MOOX_FACTOR_ENGINE_PYTHON_WORKERS='' MOOX_FACTOR_ENGINE_VIEW_READ_WORKERS='' MOOX_FACTOR_ENGINE_VIEW_READ_TIMEOUT_MS='' \
  MOOX_CONTROL_ROOT='' MOOX_STORAGE_ROOT='' PUBLIC_HOST='' TLS_MODE_RESOLVED='' BROWSER_HTTPS_PORT=9527 \
  SERVICE_HTTPS_PORT=11001 TARGET_GOOS=linux TARGET_GOARCH=amd64 bash "${REMOTE_OVERLAY_SCRIPT}" >/dev/null
! grep -Fq '"key_id":"factor"' "${REMOTE_NO_GATEWAY_ROOT}/secrets/gateway-credentials.json" || \
  fail 'remote --no-gateway overlay changed the active Gateway registry'
[[ -f "${REMOTE_NO_GATEWAY_ROOT}/secrets/gateway-factor.key" ]] && \
  ! grep -q '[^[:space:]]' "${REMOTE_NO_GATEWAY_ROOT}/secrets/gateway-factor.key" || \
  fail 'remote --no-gateway overlay changed an inactive Gateway key'
grep -Fxq 'preserve-skill-key' "${REMOTE_NO_GATEWAY_ROOT}/secrets/gateway-moox-skill.key" || \
  fail 'remote --no-gateway overlay rotated the inactive skill key'

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
