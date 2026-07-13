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
printf 'MOOX_SERVICE_AUTH_SECRET_KEY=keep-this-secret\nMOOX_SERVICE_AUTH_EXPIRE_SECONDS=1800\n' >"${DEPLOY_DIR}/secrets/service-auth.env"
printf 'keep-this-ca\n' >"${DEPLOY_DIR}/certs/caddy/root.crt"
seed_disabled_component cloudnode moox-cloudnode moox-cloudnode-cli
seed_disabled_component collector moox-collector moox-collector-cli moox-collector-scf
seed_disabled_component factor moox-factor moox-factor-cli
seed_disabled_component storage moox-storage moox-storage-cli

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
  --no-factor >/dev/null

for path in \
  cloudnode/config/keep.txt \
  collector/config/keep.txt \
  factor/config/keep.txt \
  storage/config/keep.txt \
  bin/moox-cloudnode \
  bin/moox-cloudnode-cli \
  bin/moox-collector \
  bin/moox-collector-cli \
  bin/moox-collector-scf \
  bin/moox-factor \
  bin/moox-factor-cli \
  bin/moox-storage \
  bin/moox-storage-cli
do
  if [[ ! -e "${DEPLOY_DIR}/${path}" ]]; then
    echo "expected disabled deployment artifact to be preserved: ${path}" >&2
    exit 1
  fi
done

grep -Fq 'MOOX_SERVICE_AUTH_SECRET_KEY=keep-this-secret' "${DEPLOY_DIR}/secrets/service-auth.env"
grep -Fq 'MOOX_SERVICE_AUTH_EXPIRE_SECONDS=60' "${DEPLOY_DIR}/secrets/service-auth.env"
grep -Fq 'keep-this-ca' "${DEPLOY_DIR}/certs/caddy/root.crt"

echo "disabled deployment artifacts preserved"
