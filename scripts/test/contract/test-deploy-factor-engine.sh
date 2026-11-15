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
for secret in access-factor-engine.key factor-eventbus.yaml; do
  printf 'secret-%s\n' "${secret}" >"${DIR}/secrets/${secret}"
  chmod 0600 "${DIR}/secrets/${secret}"
done

printf '#!/usr/bin/env bash\nexit 0\n' >"${TMP_ROOT}/moox-factor-engine-new"
chmod +x "${TMP_ROOT}/moox-factor-engine-new"

install_args=(
  --dir "${DIR}" --binary "${TMP_ROOT}/moox-factor-engine-new" --no-start --engine-id factor-engine@contract
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
# 引擎严格解析配置：Storage 已不需要请求体口令，不能再渲染 storage 段。
! grep -Eq '^storage:' "${config}"
grep -Eq '^compute:' "${config}"
[[ -s "${DIR}/config/trpc_go.engine.yaml" ]]
# 健康端口只听本机、不验签：不安装健康凭据，服务定义也不引用它。
[[ ! -e "${DIR}/secrets/health-auth.env" ]]
[[ -x "${DIR}/bin/moox-factor-engine" ]]

service="${TMP_ROOT}/systemd/moox-factor-engine.service"
[[ -s "${service}" ]]
grep -Fq "${DIR}/bin/moox-factor-engine" "${service}"
if grep -Fq 'health-auth.env' "${service}"; then
  echo "the engine service must not load a health credential" >&2
  exit 1
fi
if grep -Eq '(HTTPS?_PROXY|https?_proxy)[^ ]*=' "${service}"; then
  echo "the engine service must not carry proxy variables" >&2
  exit 1
fi

# 重复部署（复用已安装的二进制）照样通过。
"${ROOT}/scripts/deploy/deploy-factor-engine.sh" --dir "${DIR}" --skip-build --no-start --engine-id factor-engine@contract \
  --access-address storage.example:11004 --access-id access@storage --access-key-id assigned-factor-key-17 \
  --eventbus-url tls://control.example:4222 >/dev/null
# 已删除的 --health-env 参数要被拒绝，不能静默忽略。
if "${ROOT}/scripts/deploy/deploy-factor-engine.sh" "${install_args[@]}" --health-env "${TMP_ROOT}/health-auth.env" >/dev/null 2>&1; then
  echo "installer accepted the removed --health-env option" >&2
  exit 1
fi

chmod 0644 "${DIR}/secrets/access-factor-engine.key"
if "${ROOT}/scripts/deploy/deploy-factor-engine.sh" "${install_args[@]}" >/dev/null 2>&1; then
  echo "installer accepted a group-readable credential" >&2
  exit 1
fi
chmod 0600 "${DIR}/secrets/access-factor-engine.key"
rm "${DIR}/secrets/factor-eventbus.yaml"
if "${ROOT}/scripts/deploy/deploy-factor-engine.sh" "${install_args[@]}" >/dev/null 2>&1; then
  echo "installer accepted a missing EventBus credential" >&2
  exit 1
fi

echo 'factor engine deployment contract passed'
