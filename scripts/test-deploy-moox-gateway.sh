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

assert_contains "${BUILD}" 'gateway)'
assert_contains "${BUILD}" 'modules/gateway ./cmd/server moox-gateway'
assert_contains "${BUILD}" 'modules/gateway ./cmd/cli moox-gateway-cli'
assert_contains "${RELEASE}" 'gateway/config'
assert_contains "${RELEASE}" 'copy_binary moox-gateway'
assert_contains "${RELEASE}" 'copy_binary moox-gateway-cli'

for option in --node-id --gateway-control-url --gateway-ca-bundle --gateway-control-key-file --gateway-service-key-file --no-admin; do
  assert_contains "${DEPLOY}" "${option}"
done
assert_contains "${DEPLOY}" 'MOOX_ADMIN_NODE_ID'
assert_contains "${DEPLOY}" 'MOOX_GATEWAY_NODE_ID'
assert_contains "${DEPLOY}" 'gateway-control.env'
assert_contains "${DEPLOY}" 'gateway-service.env'
assert_contains "${DEPLOY}" 'chmod 0600'
assert_contains "${DEPLOY}" 'certs/gateway/peers.pem'
assert_contains "${DEPLOY}" 'start_gateway'
assert_contains "${DEPLOY}" 'start_admin'
assert_contains "${DEPLOY}" 'start_monitor'
assert_contains "${DEPLOY}" 'gateway) url=http://127.0.0.1:11012/readyz'
assert_contains "${DEPLOY}" 'start_service "gateway"'
assert_contains "${DEPLOY}" 'stop_service "gateway"'
assert_contains "${DEPLOY}" 'services+=(gateway)'

assert_contains "${CADDY}" 'handle /api/gateway-control/*'
assert_contains "${CADDY}" 'reverse_proxy 127.0.0.1:11000'
assert_contains "${CADDY}" 'handle /api/service/*'
assert_contains "${CADDY}" 'reverse_proxy 127.0.0.1:11002'
[[ -f "${NO_ADMIN_CADDY}" ]] || fail 'no-admin Caddyfile is missing'
assert_absent "${NO_ADMIN_CADDY}" '/api/gateway-control/'
assert_absent "${NO_ADMIN_CADDY}" 'MOOX_BROWSER_HTTPS_PORT'
assert_contains "${NO_ADMIN_CADDY}" 'handle /api/service/*'
assert_contains "${NO_ADMIN_CADDY}" 'reverse_proxy 127.0.0.1:11002'

assert_contains "${ROOT}/modules/gateway/config/trpc_go.yaml" '127.0.0.1:11002'
assert_contains "${ROOT}/modules/gateway/config/trpc_go.yaml" '127.0.0.1:11012'

command -v openssl >/dev/null 2>&1 || fail 'openssl is required for the deployment fixture'
TMP=$(mktemp -d "${TMPDIR:-/tmp}/moox-gateway-deploy.XXXXXX")
CREATED_BINARIES=()
cleanup() {
  local binary
  for binary in ${CREATED_BINARIES[@]+"${CREATED_BINARIES[@]}"}; do rm -f "${binary}"; done
  rm -rf "${TMP}"
}
trap cleanup EXIT
mkdir -p "${ROOT}/bin"
for name in moox-gateway moox-gateway-cli; do
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
openssl req -x509 -newkey rsa:2048 -nodes -subj /CN=gateway-test \
  -keyout "${TMP}/private-one.key" -out "${TMP}/root-one.crt" -days 1 >/dev/null 2>&1
openssl req -x509 -newkey rsa:2048 -nodes -subj /CN=gateway-peer-test \
  -keyout "${TMP}/private-two.key" -out "${TMP}/root-two.crt" -days 1 >/dev/null 2>&1
cat "${TMP}/root-one.crt" "${TMP}/root-two.crt" >"${TMP}/peers.pem"

deploy_fixture() {
  local bundle="$1" suffix="$2"
  "${DEPLOY}" --target localhost --dir "${TMP}/deploy-${suffix}" --stage "${TMP}/stage-${suffix}" \
    --skip-build --no-start --no-admin --no-storage --no-archive --no-eventbus \
    --no-cloudnode --no-collector --no-factor --no-monitor --local-ca skip --target-ca skip \
    --node-id gateway-test --gateway-control-url https://admin.example.com \
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

cat "${TMP}/root-one.crt" "${TMP}/root-one.crt" >"${TMP}/duplicate.pem"
cat "${TMP}/root-one.crt" >"${TMP}/malformed.pem"
printf '%s\n' '-----BEGIN CERTIFICATE-----' 'not-a-certificate' '-----END CERTIFICATE-----' >>"${TMP}/malformed.pem"
cat "${TMP}/peers.pem" "${TMP}/private-one.key" >"${TMP}/private.pem"
expect_ca_rejected "${TMP}/duplicate.pem" duplicate 'at least two distinct public CA certificates'
expect_ca_rejected "${TMP}/malformed.pem" malformed 'contains a malformed certificate'
expect_ca_rejected "${TMP}/private.pem" private 'must never contain private-key blocks'

deploy_fixture "${TMP}/peers.pem" valid >/dev/null
DEPLOYED="${TMP}/deploy-valid"

for script in start.sh stop.sh restart.sh status.sh healthcheck.sh; do
  bash -n "${DEPLOYED}/${script}" || fail "generated ${script} is invalid"
done
[[ -x "${DEPLOYED}/bin/moox-gateway" ]] || fail 'Gateway binary was not staged'
[[ -f "${DEPLOYED}/gateway/config/app.yaml" ]] || fail 'Gateway config was not staged'
grep -Fq 'id: gateway-test' "${DEPLOYED}/gateway/config/app.yaml" || fail 'node ID was not rendered'
grep -Fq 'base_url: https://admin.example.com' "${DEPLOYED}/gateway/config/app.yaml" || fail 'control URL was not rendered'
[[ ! -e "${DEPLOYED}/admin" && ! -e "${DEPLOYED}/bin/moox-admin" ]] || fail 'no-admin staged Admin artifacts'
[[ ! -e "${DEPLOYED}/bin/moox-web-host" && ! -e "${DEPLOYED}/secrets/admin-jwt.env" ]] || fail 'no-admin staged browser or Admin credentials'
cmp -s "${TMP}/peers.pem" "${DEPLOYED}/certs/gateway/peers.pem" || fail 'public peer CA was not installed'
! grep -Rqs -- 'PRIVATE KEY' "${DEPLOYED}/certs" || fail 'a private CA key was staged'
for secret in gateway-control.env gateway-service.env gateway-control.key gateway-service.key; do
  mode=$(stat -f '%Lp' "${DEPLOYED}/secrets/${secret}" 2>/dev/null || stat -c '%a' "${DEPLOYED}/secrets/${secret}")
  [[ "${mode}" == 600 ]] || fail "${secret} mode is ${mode}, want 600"
done
grep -Fq 'start_admin' "${DEPLOYED}/start.sh" || fail 'central lifecycle function was omitted'
grep -Fq 'start_gateway' "${DEPLOYED}/start.sh" || fail 'Gateway lifecycle function was omitted'
grep -Fq 'start_monitor' "${DEPLOYED}/start.sh" || fail 'Monitor lifecycle function was omitted'
admin_line=$(grep -n 'start_admin$' "${DEPLOYED}/start.sh" | tail -1 | cut -d: -f1)
gateway_line=$(grep -n 'start_gateway$' "${DEPLOYED}/start.sh" | head -1 | cut -d: -f1)
monitor_line=$(grep -n 'start_monitor$' "${DEPLOYED}/start.sh" | tail -1 | cut -d: -f1)
(( admin_line < gateway_line && gateway_line < monitor_line )) || fail 'startup order is not Admin -> Gateway -> Monitor'

printf 'PASS: Gateway build, package, lifecycle, secret, CA, and Caddy contracts\n'
