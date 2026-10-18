#!/usr/bin/env bash
# 反向隧道安装脚本的契约：渲染的 systemd 服务、受限 authorized_keys 行、参数校验。不联网、不启动服务。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)"
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/moox-control-tunnel.XXXXXX")"
trap 'rm -rf "${TMP_ROOT}"' EXIT
DIR="${TMP_ROOT}/tunnel"
export MOOX_SYSTEMD_USER_DIR="${TMP_ROOT}/systemd"

# 已核对的 control 主机密钥条目（用一把临时密钥充当）。
ssh-keygen -q -t ed25519 -N '' -f "${TMP_ROOT}/hostkey"
printf '106.53.107.122 %s\n' "$(cut -d' ' -f1-2 "${TMP_ROOT}/hostkey.pub")" >"${TMP_ROOT}/known_hosts"

args=(--control ubuntu@106.53.107.122 --known-hosts "${TMP_ROOT}/known_hosts" --dir "${DIR}" --no-start)
line="$("${ROOT}/scripts/deploy/deploy-control-tunnel.sh" "${args[@]}" 2>/dev/null)"

service="${TMP_ROOT}/systemd/moox-control-tunnel.service"
[[ -s "${service}" ]]
DIR="$(cd "${DIR}" && pwd -P)"
for port in 11012 11003 11425; do
  grep -Fq -- "-R 127.0.1.103:${port}:127.0.0.1:${port}" "${service}"
  [[ "${line}" == *"permitlisten=\"127.0.1.103:${port}\""* ]]
done
grep -Fq -- "-o ExitOnForwardFailure=yes" "${service}"
grep -Fq -- "-o StrictHostKeyChecking=yes" "${service}"
grep -Fq -- "-o UserKnownHostsFile=${DIR}/known_hosts" "${service}"
grep -Fq -- "-i ${DIR}/id_ed25519" "${service}"
grep -Fq "Restart=always" "${service}"
grep -Fq "ubuntu@106.53.107.122" "${service}"
! grep -q '__' "${service}"
[[ "${line}" == restrict,port-forwarding,* ]]
[[ "${line}" == *'command="/bin/false" ssh-ed25519 '* ]]
[[ "$(stat -f '%Lp' "${DIR}/id_ed25519" 2>/dev/null || stat -c '%a' "${DIR}/id_ed25519")" == 600 ]]

# 重复安装复用同一把密钥。
first_key="$(cat "${DIR}/id_ed25519.pub")"
"${ROOT}/scripts/deploy/deploy-control-tunnel.sh" "${args[@]}" >/dev/null 2>&1
[[ "$(cat "${DIR}/id_ed25519.pub")" == "${first_key}" ]]

reject() {
  if "${ROOT}/scripts/deploy/deploy-control-tunnel.sh" "$@" >/dev/null 2>&1; then
    echo "installer accepted: $*" >&2
    exit 1
  fi
}
reject --control ubuntu@106.53.107.122 --dir "${DIR}" --no-start                                  # 没有主机密钥核对
reject "${args[@]}" --bind 127.0.0.1                                                              # 与 control 自己的端口冲突
reject "${args[@]}" --bind 10.0.0.5                                                               # 不是回环地址
reject "${args[@]}" --ports 11012,abc
reject --control ubuntu@106.53.107.122 --control-fingerprint SHA256:short --dir "${DIR}" --no-start

echo 'control tunnel deployment contract passed'
