#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
COMMAND=${1:-}; shift || true
TARGET= DEPLOY_DIR= OUTPUT= CA_FILE= EXPECTED= NON_INTERACTIVE=0
while [[ $# -gt 0 ]]; do case "$1" in --target) TARGET=${2:?}; shift 2;; --deploy-dir) DEPLOY_DIR=${2:?}; shift 2;; --output) OUTPUT=${2:?}; shift 2;; --ca-file) CA_FILE=${2:?}; shift 2;; --expected-fingerprint) EXPECTED=${2:?}; shift 2;; --non-interactive) NON_INTERACTIVE=1; shift;; *) echo "unknown option: $1" >&2; exit 2;; esac; done
reject_key_path() { [[ "$1" != *.key && "$1" != *root.key* ]] || { echo 'private-key paths are forbidden' >&2; exit 1; }; }
inspect() { grep -q -- 'PRIVATE KEY' "$1" && { echo 'private keys are forbidden' >&2; exit 1; }; openssl x509 -in "$1" -noout -subject -dates -fingerprint -sha256; openssl x509 -in "$1" -noout -text | grep -Eq 'CA:TRUE' || { echo 'certificate is not a CA' >&2; exit 1; }; }
case "${COMMAND}" in
  fetch)
    [[ -n "${TARGET}" && -n "${DEPLOY_DIR}" && -n "${OUTPUT}" ]] || { echo 'fetch requires --target, --deploy-dir, and --output' >&2; exit 2; }
    reject_key_path "${OUTPUT}"; remote="${DEPLOY_DIR%/}/data/caddy/caddy/pki/authorities/local/root.crt"
    mkdir -p "$(dirname "${OUTPUT}")"
    if [[ "${TARGET}" == localhost || "${TARGET}" == 127.0.0.1 ]]; then cp "${remote}" "${OUTPUT}"; else scp -o BatchMode=yes -o ConnectTimeout=10 "${TARGET}:$(printf %q "${remote}")" "${OUTPUT}"; fi
    chmod 0644 "${OUTPUT}"; inspect "${OUTPUT}" >/dev/null
    actual=$(openssl x509 -in "${OUTPUT}" -noout -fingerprint -sha256 | cut -d= -f2)
    [[ -z "${EXPECTED}" || "${actual}" == "${EXPECTED}" ]] || { rm -f "${OUTPUT}"; echo 'CA fingerprint mismatch' >&2; exit 1; }
    printf '%s\n' "${actual}";;
  inspect) [[ -n "${CA_FILE}" ]] || exit 2; inspect "${CA_FILE}";;
  install) [[ -n "${CA_FILE}" ]] || exit 2; inspect "${CA_FILE}"; exec "${ROOT}/scripts/install-caddy-ca.sh" --ca-file "${CA_FILE}";;
  install-target)
    [[ -n "${TARGET}" && -n "${DEPLOY_DIR}" ]] || { echo 'install-target requires --target and --deploy-dir' >&2; exit 2; }
    if [[ "${TARGET}" == localhost || "${TARGET}" == 127.0.0.1 || "${TARGET}" == ::1 ]]; then
      local_deploy_dir="${DEPLOY_DIR}"
      case "${local_deploy_dir}" in '~') local_deploy_dir="${HOME}";; '~/'*) local_deploy_dir="${HOME}/${local_deploy_dir#\~/}";; esac
      if [[ "${NON_INTERACTIVE}" == 1 ]]; then export MOOX_CA_SUDO_NONINTERACTIVE=1; fi
      exec "${ROOT}/scripts/install-caddy-ca.sh" --ca-file "${local_deploy_dir%/}/certs/caddy/root.crt"
    fi
    remote_script='deploy_dir=$1
case "$deploy_dir" in "~") deploy_dir=$HOME;; "~/"*) deploy_dir="$HOME/${deploy_dir#\~/}";; esac
exec "$deploy_dir/lib/install-caddy-ca.sh" --ca-file "$deploy_dir/certs/caddy/root.crt"'
    printf -v remote_command 'bash -c %q _ %q' "${remote_script}" "${DEPLOY_DIR}"
    if [[ "${NON_INTERACTIVE}" == 1 ]]; then
      remote_command="MOOX_CA_SUDO_NONINTERACTIVE=1 ${remote_command}"
      exec ssh -o BatchMode=yes -o ConnectTimeout=10 "${TARGET}" "${remote_command}"
    fi
    exec ssh -t -o ConnectTimeout=10 "${TARGET}" "${remote_command}"
    ;;
  status)
    [[ -n "${CA_FILE}" ]] || exit 2
    inspect "${CA_FILE}" >/dev/null
    trusted=false
    if "${ROOT}/scripts/install-caddy-ca.sh" --ca-file "${CA_FILE}" --check; then trusted=true; fi
    printf '{"fingerprint":"%s","valid_ca":true,"trusted":%s}\n' \
      "$(openssl x509 -in "${CA_FILE}" -noout -fingerprint -sha256 | cut -d= -f2)" "${trusted}"
    ;;
  trust-help)
    [[ -n "${CA_FILE}" ]] || exit 2; inspect "${CA_FILE}" >/dev/null
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
          printf 'Linux: no supported trust-store command found\n'
        fi ;;
      MINGW*|MSYS*|CYGWIN*)
        printf 'Windows: powershell.exe -NoProfile -NonInteractive -Command %q %q\n' \
          'Import-Certificate -FilePath $args[0] -CertStoreLocation Cert:\\CurrentUser\\Root | Out-Null' "${CA_FILE}" ;;
      *) printf 'Unsupported OS %q; install this CA in the browser system trust store\n' "${platform}" ;;
    esac
    printf 'Check: %q status --ca-file %q\n' "${ROOT}/skills/moox/scripts/caddy-ca.sh" "${CA_FILE}"
    printf 'curl --cacert %q https://HOST:9527/\n' "${CA_FILE}"
    printf 'Applications: set MOOX_SERVICE_GATEWAY_CA_FILE=%q; never disable TLS verification.\n' "${CA_FILE}";;
  *) echo 'command required: fetch|inspect|install|install-target|status|trust-help' >&2; exit 2;;
esac
