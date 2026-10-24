#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)"
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/moox-factor-engine-deploy.XXXXXX")"
trap 'rm -rf "${TMP_ROOT}"' EXIT
DIR="${TMP_ROOT}/engine"
mkdir -p "${DIR}/bin" "${DIR}/secrets"
DIR="$(cd "${DIR}" && pwd -P)"
printf '#!/usr/bin/env bash\nexit 0\n' >"${DIR}/bin/moox-factor-engine"
chmod +x "${DIR}/bin/moox-factor-engine"
for secret in access-factor-engine.key storage-primary-auth.secret factor-eventbus.yaml; do
  printf 'secret-%s\n' "${secret}" >"${DIR}/secrets/${secret}"
  chmod 0600 "${DIR}/secrets/${secret}"
done

health_env="${TMP_ROOT}/health-auth.env"
printf 'MOOX_HEALTH_AUTH_VERSION=moox-health-v1\nMOOX_HEALTH_AUTH_ACCESS_KEY=health-access-17\nMOOX_HEALTH_AUTH_SECRET_KEY=%s\n' "$(printf 'h%.0s' {1..64})" >"${health_env}"
chmod 0600 "${health_env}"
printf '#!/usr/bin/env bash\nexit 0\n' >"${TMP_ROOT}/moox-factor-engine-new"
chmod +x "${TMP_ROOT}/moox-factor-engine-new"

install_args=(
  --dir "${DIR}" --binary "${TMP_ROOT}/moox-factor-engine-new" --health-env "${health_env}" --no-start --engine-id factor-engine@contract
  --access-address storage.example:11004 --access-id access@storage
  --access-key-id assigned-factor-key-17
  --eventbus-url tls://control.example:4222
)
export MOOX_SYSTEMD_USER_DIR="${TMP_ROOT}/systemd"
HTTPS_PROXY=http://127.0.0.1:7897 "${ROOT}/scripts/deploy/deploy-factor-engine.sh" "${install_args[@]}" >/dev/null

config="${DIR}/config/engine.yaml"
grep -Fq 'id: factor-engine@contract' "${config}"
grep -Fq 'key_id: "assigned-factor-key-17"' "${config}"
grep -Fq 'access_address: "storage.example:11004"' "${config}"
grep -Fq 'access_id: "access@storage"' "${config}"
grep -Fq "key_file: ${DIR}/secrets/access-factor-engine.key" "${config}"
grep -Fq 'tls://control.example:4222' "${config}"
! grep -Eq '^database:' "${config}"
grep -Eq '^compute:' "${config}"
[[ -s "${DIR}/config/trpc_go.engine.yaml" ]]
# 健康凭据按传入的文件安装，权限 0600。
grep -Fq 'MOOX_HEALTH_AUTH_ACCESS_KEY=health-access-17' "${DIR}/secrets/health-auth.env"
[[ "$(stat -f '%Lp' "${DIR}/secrets/health-auth.env" 2>/dev/null || stat -c '%a' "${DIR}/secrets/health-auth.env")" == 600 ]]
[[ -x "${DIR}/bin/moox-factor-engine" ]]

service="${TMP_ROOT}/systemd/moox-factor-engine.service"
[[ -s "${service}" ]]
grep -Fq "${DIR}/bin/moox-factor-engine" "${service}"
if grep -Eq '(HTTPS?_PROXY|https?_proxy)[^ ]*=' "${service}"; then
  echo "the engine service must not carry proxy variables" >&2
  exit 1
fi

# 重复部署不带 --health-env 时沿用已安装的健康凭据。
"${ROOT}/scripts/deploy/deploy-factor-engine.sh" --dir "${DIR}" --skip-build --no-start --engine-id factor-engine@contract \
  --access-address storage.example:11004 --access-id access@storage --access-key-id assigned-factor-key-17 \
  --eventbus-url tls://control.example:4222 >/dev/null
grep -Fq 'MOOX_HEALTH_AUTH_ACCESS_KEY=health-access-17' "${DIR}/secrets/health-auth.env"

# 健康凭据文件权限不对、缺变量都要拒绝。
loose="${TMP_ROOT}/loose.env"
cp "${health_env}" "${loose}"
chmod 0644 "${loose}"
if "${ROOT}/scripts/deploy/deploy-factor-engine.sh" --dir "${DIR}" --binary "${TMP_ROOT}/moox-factor-engine-new" --health-env "${loose}" --no-start \
  --access-address storage.example:11004 --access-id access@storage --access-key-id assigned-factor-key-17 --eventbus-url tls://control.example:4222 >/dev/null 2>&1; then
  echo "installer accepted a group-readable health credential" >&2
  exit 1
fi
incomplete="${TMP_ROOT}/incomplete.env"
printf 'MOOX_HEALTH_AUTH_VERSION=moox-health-v1\n' >"${incomplete}"
chmod 0600 "${incomplete}"
if "${ROOT}/scripts/deploy/deploy-factor-engine.sh" --dir "${DIR}" --binary "${TMP_ROOT}/moox-factor-engine-new" --health-env "${incomplete}" --no-start \
  --access-address storage.example:11004 --access-id access@storage --access-key-id assigned-factor-key-17 --eventbus-url tls://control.example:4222 >/dev/null 2>&1; then
  echo "installer accepted an incomplete health credential" >&2
  exit 1
fi

chmod 0644 "${DIR}/secrets/access-factor-engine.key"
if "${ROOT}/scripts/deploy/deploy-factor-engine.sh" "${install_args[@]}" >/dev/null 2>&1; then
  echo "installer accepted a group-readable credential" >&2
  exit 1
fi
chmod 0600 "${DIR}/secrets/access-factor-engine.key"
rm "${DIR}/secrets/storage-primary-auth.secret"
if "${ROOT}/scripts/deploy/deploy-factor-engine.sh" "${install_args[@]}" >/dev/null 2>&1; then
  echo "installer accepted a missing Storage auth secret" >&2
  exit 1
fi

echo 'factor engine deployment contract passed'
