#!/usr/bin/env bash
# 控制台代理（Caddy internal CA）根证书的取回、检查和安装，只处理公开的根证书，拒绝私钥。
#
#   caddy-ca.sh fetch --target <user@host> --deploy-dir <control 的部署根目录> --output <文件> [--expected-fingerprint <SHA256 指纹>]
#   caddy-ca.sh inspect --ca-file <文件>
#   caddy-ca.sh install --ca-file <文件> [--non-interactive]
#   caddy-ca.sh status --ca-file <文件>
#   caddy-ca.sh trust-help --ca-file <文件>
#
# 只有控制台代理使用 internal 证书时才需要它，用来让浏览器信任 https://<control>:9527。服务之间的 TLS 用 MooX 私有 CA
# （certs/moox-ca.crt），与这里无关。日常用 moox-cli setup trust-browser 即可，本脚本用于手工取回和核对指纹。
set -euo pipefail
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
COMMAND=${1:-}; shift || true
TARGET= DEPLOY_DIR= OUTPUT= CA_FILE= EXPECTED= NON_INTERACTIVE=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --target) TARGET=${2:?}; shift 2;;
    --deploy-dir) DEPLOY_DIR=${2:?}; shift 2;;
    --output) OUTPUT=${2:?}; shift 2;;
    --ca-file) CA_FILE=${2:?}; shift 2;;
    --expected-fingerprint) EXPECTED=${2:?}; shift 2;;
    --non-interactive) NON_INTERACTIVE=1; shift;;
    *) echo "未知参数：$1" >&2; exit 2;;
  esac
done
reject_key_path() { [[ "$1" != *.key && "$1" != *root.key* ]] || { echo '不能使用私钥路径' >&2; exit 1; }; }
inspect() {
  grep -q -- 'PRIVATE KEY' "$1" && { echo '文件里不能有私钥' >&2; exit 1; }
  openssl x509 -in "$1" -noout -subject -dates -fingerprint -sha256
  openssl x509 -in "$1" -noout -text | grep -Eq 'CA:TRUE' || { echo '证书不是 CA 证书' >&2; exit 1; }
}
case "${COMMAND}" in
  fetch)
    [[ -n "${TARGET}" && -n "${DEPLOY_DIR}" && -n "${OUTPUT}" ]] || { echo 'fetch 需要 --target、--deploy-dir 和 --output' >&2; exit 2; }
    reject_key_path "${OUTPUT}"
    remote="${DEPLOY_DIR%/}/data/console-proxy/caddy/pki/authorities/local/root.crt"
    mkdir -p "$(dirname "${OUTPUT}")"
    if [[ "${TARGET}" == localhost || "${TARGET}" == 127.0.0.1 ]]; then
      cp "${remote}" "${OUTPUT}"
    else
      scp -o BatchMode=yes -o ConnectTimeout=10 "${TARGET}:$(printf %q "${remote}")" "${OUTPUT}"
    fi
    chmod 0644 "${OUTPUT}"; inspect "${OUTPUT}" >/dev/null
    actual=$(openssl x509 -in "${OUTPUT}" -noout -fingerprint -sha256 | cut -d= -f2)
    [[ -z "${EXPECTED}" || "${actual}" == "${EXPECTED}" ]] || { rm -f "${OUTPUT}"; echo 'CA 指纹不一致，已删除下载的文件' >&2; exit 1; }
    printf '%s\n' "${actual}";;
  inspect)
    [[ -n "${CA_FILE}" ]] || { echo 'inspect 需要 --ca-file' >&2; exit 2; }
    inspect "${CA_FILE}";;
  install)
    [[ -n "${CA_FILE}" ]] || { echo 'install 需要 --ca-file' >&2; exit 2; }
    inspect "${CA_FILE}"
    # 没有免密 sudo 时直接以退出码 77 失败，而不是等待输入密码（供 Agent 使用）。
    if [[ "${NON_INTERACTIVE}" == 1 ]]; then export MOOX_CA_SUDO_NONINTERACTIVE=1; fi
    exec "${ROOT}/scripts/deploy/install-caddy-ca.sh" --ca-file "${CA_FILE}";;
  status)
    [[ -n "${CA_FILE}" ]] || { echo 'status 需要 --ca-file' >&2; exit 2; }
    inspect "${CA_FILE}" >/dev/null
    trusted=false
    if "${ROOT}/scripts/deploy/install-caddy-ca.sh" --ca-file "${CA_FILE}" --check; then trusted=true; fi
    printf '{"fingerprint":"%s","valid_ca":true,"trusted":%s}\n' \
      "$(openssl x509 -in "${CA_FILE}" -noout -fingerprint -sha256 | cut -d= -f2)" "${trusted}";;
  trust-help)
    [[ -n "${CA_FILE}" ]] || { echo 'trust-help 需要 --ca-file' >&2; exit 2; }
    inspect "${CA_FILE}" >/dev/null
    platform=$(uname -s)
    case "${platform}" in
      Darwin*)
        printf 'macOS: sudo security add-trusted-cert -d -r trustRoot -p ssl -k /Library/Keychains/System.keychain %q\n' "${CA_FILE}" ;;
      Linux*)
        if command -v update-ca-certificates >/dev/null 2>&1; then
          printf 'Debian/Ubuntu: sudo cp %q /usr/local/share/ca-certificates/moox-caddy-root.crt && sudo update-ca-certificates\n' "${CA_FILE}"
        elif command -v update-ca-trust >/dev/null 2>&1; then
          printf 'RHEL/Fedora: sudo cp %q /etc/pki/ca-trust/source/anchors/moox-caddy-root.crt && sudo update-ca-trust\n' "${CA_FILE}"
        else
          printf 'Linux: 没有找到支持的系统信任库命令\n'
        fi ;;
      MINGW*|MSYS*|CYGWIN*)
        printf 'Windows: powershell.exe -NoProfile -NonInteractive -Command %q %q\n' \
          'Import-Certificate -FilePath $args[0] -CertStoreLocation Cert:\\CurrentUser\\Root | Out-Null' "${CA_FILE}" ;;
      *) printf '不支持的系统 %q：请把这张 CA 证书安装到浏览器使用的系统信任库\n' "${platform}" ;;
    esac
    printf '检查：%q status --ca-file %q\n' "${ROOT}/skills/moox/scripts/caddy-ca.sh" "${CA_FILE}"
    printf 'curl --cacert %q https://HOST:9527/\n' "${CA_FILE}"
    printf '安装后重启浏览器；不要关闭 TLS 校验。\n';;
  *) echo '需要子命令：fetch|inspect|install|status|trust-help' >&2; exit 2;;
esac
