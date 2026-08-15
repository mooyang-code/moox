#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
if [[ "$(basename "${SCRIPT_DIR}")" == "contract" ]]; then
  ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd -P)"
else
  ROOT="$(cd "${SCRIPT_DIR}/.." && pwd -P)"
fi
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/moox-deploy-preserve.XXXXXX")"
DEPLOY_DIR="${TMP_ROOT}/deploy"
STAGE_DIR="${TMP_ROOT}/stage"
CREATED_BINARIES=()

cleanup() {
  rm -rf "${TMP_ROOT}"
  for path in "${CREATED_BINARIES[@]+"${CREATED_BINARIES[@]}"}"; do
    rm -f "${path}"
  done
}
trap cleanup EXIT

ensure_required_binary() {
  local name="$1"
  local path="${ROOT}/bin/${name}"
  if [[ -x "${path}" ]]; then
    return
  fi
  mkdir -p "${ROOT}/bin"
  cat >"${path}" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
  chmod +x "${path}"
  CREATED_BINARIES+=("${path}")
}

seed_disabled_component() {
  local dir="$1"
  shift
  mkdir -p "${DEPLOY_DIR}/${dir}/config" "${DEPLOY_DIR}/bin"
  printf 'keep-%s\n' "${dir}" >"${DEPLOY_DIR}/${dir}/config/keep.txt"
  local binary
  for binary in "$@"; do
    printf '#!/usr/bin/env bash\nexit 0\n' >"${DEPLOY_DIR}/bin/${binary}"
    chmod +x "${DEPLOY_DIR}/bin/${binary}"
  done
}

ensure_required_binary moox-admin
ensure_required_binary moox-admin-cli
ensure_required_binary moox-cli
ensure_required_binary moox-eventbus
ensure_required_binary moox-archive
ensure_required_binary moox-archive-cli
ensure_required_binary moox-gateway
ensure_required_binary moox-gateway-cli
ensure_required_binary moox-factor
ensure_required_binary moox-factor-cli

HEALTH_SECRET_CLI="${TMP_ROOT}/moox-admin-cli"
printf '#!/usr/bin/env bash\nprintf '\''{"status":"ok","bytes":32,"secret":"%%064d"}\\n'\'' 0\n' >"${HEALTH_SECRET_CLI}"
chmod +x "${HEALTH_SECRET_CLI}"
export MOOX_HEALTH_SECRET_CLI="${HEALTH_SECRET_CLI}"
export HOME="${TMP_ROOT}/home"
mkdir -p "${HOME}/.config/moox/credentials"
printf 'fixture-encryption-key' >"${HOME}/.config/moox/credentials/admin-encryption-key"
chmod 0600 "${HOME}/.config/moox/credentials/admin-encryption-key"

mkdir -p "${DEPLOY_DIR}/data" "${DEPLOY_DIR}/logs" "${DEPLOY_DIR}/run"
: >"${DEPLOY_DIR}/data/admin.db"
mkdir -p "${DEPLOY_DIR}/secrets" "${DEPLOY_DIR}/certs/caddy"
printf 'keep-this-ca\n' >"${DEPLOY_DIR}/certs/caddy/root.crt"
seed_disabled_component cloudnode moox-cloudnode moox-cloudnode-cli
seed_disabled_component collector moox-collector moox-collector-cli
seed_disabled_component factor moox-factor moox-factor-cli
seed_disabled_component strategy moox-strategy moox-strategy-cli
seed_disabled_component trade moox-trade moox-trade-cli
seed_disabled_component storage moox-storage moox-storage-cli
printf 'keep-storage-provenance\n' >"${DEPLOY_DIR}/build-provenance.json"
printf '#!/usr/bin/env bash\necho keep-storage-reset\n' >"${DEPLOY_DIR}/reset-storage-view-indexes.sh"
chmod +x "${DEPLOY_DIR}/reset-storage-view-indexes.sh"
mkdir -p "${DEPLOY_DIR}/python-runtime/moox_pyruntime"
printf 'keep-runtime\n' >"${DEPLOY_DIR}/python-runtime/moox_pyruntime/keep.txt"

printf 'control-secret' >"${TMP_ROOT}/control.key"
printf 'service-secret' >"${TMP_ROOT}/service.key"
chmod 0600 "${TMP_ROOT}/control.key" "${TMP_ROOT}/service.key"
openssl req -x509 -newkey rsa:2048 -nodes -subj /CN=preserve-one \
  -keyout "${TMP_ROOT}/ca-one.key" -out "${TMP_ROOT}/ca-one.crt" -days 1 >/dev/null 2>&1
openssl req -x509 -newkey rsa:2048 -nodes -subj /CN=preserve-two \
  -keyout "${TMP_ROOT}/ca-two.key" -out "${TMP_ROOT}/ca-two.crt" -days 1 >/dev/null 2>&1
cat "${TMP_ROOT}/ca-one.crt" "${TMP_ROOT}/ca-two.crt" >"${TMP_ROOT}/peers.pem"

"${ROOT}/scripts/deploy-moox.sh" \
  --target localhost \
  --dir "${DEPLOY_DIR}" \
  --stage "${STAGE_DIR}" \
  --skip-build \
  --no-start \
  --no-storage \
  --no-web-host \
  --no-cloudnode \
  --no-collector \
  --no-factor --no-strategy --no-trade \
  --no-monitor \
  --node-id preserve-test \
  --gateway-control-url http://127.0.0.1:11000 \
  --gateway-ca-bundle "${TMP_ROOT}/peers.pem" \
  --gateway-control-key-file "${TMP_ROOT}/control.key" \
  --gateway-service-key-file "${TMP_ROOT}/service.key" >/dev/null

for path in \
  cloudnode/config/keep.txt \
  collector/config/keep.txt \
  factor/config/keep.txt \
  strategy/config/keep.txt \
  trade/config/keep.txt \
  python-runtime/moox_pyruntime/keep.txt \
  storage/config/keep.txt \
  build-provenance.json \
  reset-storage-view-indexes.sh \
  bin/moox-cloudnode \
  bin/moox-cloudnode-cli \
  bin/moox-collector \
  bin/moox-collector-cli \
  bin/moox-factor \
  bin/moox-factor-cli \
  bin/moox-strategy \
  bin/moox-strategy-cli \
  bin/moox-trade \
  bin/moox-trade-cli \
  bin/moox-storage \
  bin/moox-storage-cli
do
  if [[ ! -e "${DEPLOY_DIR}/${path}" ]]; then
    echo "expected disabled deployment artifact to be preserved: ${path}" >&2
    exit 1
  fi
done

grep -Fq 'keep-this-ca' "${DEPLOY_DIR}/certs/caddy/root.crt"

# A component-only update overlays the selected component onto an existing
# deployment. It must not remove the control plane or rotate its credentials.
printf 'keep-admin-jwt\n' >"${DEPLOY_DIR}/secrets/admin-jwt.env"
printf 'MOOX_HEALTH_AUTH_VERSION=moox-health-v1\nMOOX_HEALTH_AUTH_ACCESS_KEY=monitor\nMOOX_HEALTH_AUTH_SECRET_KEY=keep-health\n' >"${DEPLOY_DIR}/secrets/health-auth.env"
printf 'keep-storage-node-auth\n' >"${DEPLOY_DIR}/secrets/storage-node-auth.env"
printf 'MOOX_STORAGE_PRIMARY_AUTH_SECRET=keep-primary\nMOOX_STORAGE_VIEW_AUTH_SECRET=keep-view\n' >"${DEPLOY_DIR}/secrets/storage-internal-auth.env"
printf "MOOX_CLS_ACCOUNT_ID='acct-existing'\nMOOX_CLS_REGION='ap-guangzhou'\nMOOX_CLS_HOST='ap-guangzhou.cls.tencentyun.com'\nMOOX_CLS_LOGSET_ID='logset-existing'\nMOOX_CLS_TOPIC_ID='topic-existing'\n" >"${DEPLOY_DIR}/config/resources.env"
chmod 0644 "${DEPLOY_DIR}/config/resources.env"
mkdir -p "${DEPLOY_DIR}/config/caddy"
printf 'keep-edge-config\n' >"${DEPLOY_DIR}/config/caddy/edge.env"
for path in start.sh stop.sh status.sh healthcheck.sh; do
  cp "${DEPLOY_DIR}/${path}" "${TMP_ROOT}/${path}.before"
done
grep -Fxq 'MOOX_INSTALLED_WITH_FACTOR=0' "${DEPLOY_DIR}/config/components.env"
grep -Fxq "MOOX_CLS_TOPIC_ID='topic-existing'" "${DEPLOY_DIR}/config/resources.env"
for path in \
  secrets/gateway-control.key \
  secrets/gateway-service.key \
  secrets/gateway-credentials.json \
  secrets/storage-node-auth.env \
  secrets/storage-internal-auth.env \
  secrets/health-auth.env \
  certs/gateway/peers.pem
do
  cp "${DEPLOY_DIR}/${path}" "${TMP_ROOT}/$(basename "${path}").before"
done
cp "${HOME}/.config/moox/credentials/admin-encryption-key" "${TMP_ROOT}/admin-encryption-key.before"

MOOX_STORAGE_PRIMARY_AUTH_SECRET=primary-secret \
MOOX_STORAGE_VIEW_AUTH_SECRET=view-secret \
"${ROOT}/scripts/deploy-moox.sh" \
  --target localhost \
  --dir "${DEPLOY_DIR}" \
  --stage "${TMP_ROOT}/factor-stage" \
  --skip-build \
  --no-start \
  --component-overlay \
  --no-admin \
  --no-gateway \
  --no-storage \
  --no-archive \
  --no-eventbus \
  --no-web-host \
  --no-cloudnode \
  --no-collector \
  --no-strategy \
  --no-trade \
  --no-monitor \
  --node-id preserve-test \
  --gateway-control-url http://127.0.0.1:11000 \
  --gateway-ca-bundle "${TMP_ROOT}/peers.pem" \
  --gateway-control-key-file "${TMP_ROOT}/control.key" \
  --gateway-service-key-file "${TMP_ROOT}/service.key" >/dev/null

for path in admin gateway bin/moox-admin bin/moox-admin-cli bin/moox-gateway bin/moox-gateway-cli; do
  [[ -e "${DEPLOY_DIR}/${path}" ]] || {
    echo "component-only update removed control artifact: ${path}" >&2
    exit 1
  }
done
grep -Fxq 'keep-admin-jwt' "${DEPLOY_DIR}/secrets/admin-jwt.env"
grep -Fxq 'keep-edge-config' "${DEPLOY_DIR}/config/caddy/edge.env"
for path in start.sh stop.sh status.sh healthcheck.sh; do
  cmp "${TMP_ROOT}/${path}.before" "${DEPLOY_DIR}/${path}" || {
    echo "component-only update replaced shared lifecycle script: ${path}" >&2
    exit 1
  }
done
grep -Fxq 'MOOX_INSTALLED_WITH_FACTOR=1' "${DEPLOY_DIR}/config/components.env"
grep -Fxq 'MOOX_INSTALLED_WITH_ADMIN=1' "${DEPLOY_DIR}/config/components.env"
grep -Fxq 'MOOX_INSTALLED_WITH_GATEWAY=1' "${DEPLOY_DIR}/config/components.env"
grep -Fxq "MOOX_CLS_TOPIC_ID='topic-existing'" "${DEPLOY_DIR}/config/resources.env"
for path in start.sh stop.sh status.sh healthcheck.sh; do
  grep -Fq 'config/components.env' "${DEPLOY_DIR}/${path}" || {
    echo "component-only update left lifecycle without component inventory support: ${path}" >&2
    exit 1
  }
done
for path in \
  secrets/gateway-control.key \
  secrets/gateway-service.key \
  secrets/gateway-credentials.json \
  secrets/storage-node-auth.env \
  secrets/storage-internal-auth.env \
  secrets/health-auth.env \
  certs/gateway/peers.pem
do
  cmp "${TMP_ROOT}/$(basename "${path}").before" "${DEPLOY_DIR}/${path}" || {
    echo "component-only update replaced shared trust material: ${path}" >&2
    exit 1
  }
done
cmp "${TMP_ROOT}/admin-encryption-key.before" "${HOME}/.config/moox/credentials/admin-encryption-key"
[[ ! -e "${DEPLOY_DIR}/factor/config/keep.txt" ]] || {
  echo "component-only update did not replace the selected Factor component" >&2
  exit 1
}

echo "disabled deployment artifacts preserved"
