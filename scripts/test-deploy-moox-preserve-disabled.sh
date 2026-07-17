#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
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
seed_disabled_component collector moox-collector moox-collector-cli moox-collector-scf
seed_disabled_component factor moox-factor moox-factor-cli
seed_disabled_component strategy moox-strategy moox-strategy-cli
seed_disabled_component storage moox-storage moox-storage-cli
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
  --no-factor --no-strategy \
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
  python-runtime/moox_pyruntime/keep.txt \
  storage/config/keep.txt \
  bin/moox-cloudnode \
  bin/moox-cloudnode-cli \
  bin/moox-collector \
  bin/moox-collector-cli \
  bin/moox-collector-scf \
  bin/moox-factor \
  bin/moox-factor-cli \
  bin/moox-strategy \
  bin/moox-strategy-cli \
  bin/moox-storage \
  bin/moox-storage-cli
do
  if [[ ! -e "${DEPLOY_DIR}/${path}" ]]; then
    echo "expected disabled deployment artifact to be preserved: ${path}" >&2
    exit 1
  fi
done

grep -Fq 'keep-this-ca' "${DEPLOY_DIR}/certs/caddy/root.crt"

echo "disabled deployment artifacts preserved"
