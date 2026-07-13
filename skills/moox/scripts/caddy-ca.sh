#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
COMMAND=${1:-}; shift || true
TARGET= DEPLOY_DIR= OUTPUT= CA_FILE= EXPECTED=
while [[ $# -gt 0 ]]; do case "$1" in --target) TARGET=${2:?}; shift 2;; --deploy-dir) DEPLOY_DIR=${2:?}; shift 2;; --output) OUTPUT=${2:?}; shift 2;; --ca-file) CA_FILE=${2:?}; shift 2;; --expected-fingerprint) EXPECTED=${2:?}; shift 2;; *) echo "unknown option: $1" >&2; exit 2;; esac; done
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
    printf 'macOS: sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain %q\n' "${CA_FILE}"
    printf 'Windows: Import-Certificate -FilePath %q -CertStoreLocation Cert:\\CurrentUser\\Root\n' "${CA_FILE}"
    printf 'Debian/Ubuntu: sudo cp %q /usr/local/share/ca-certificates/moox.crt && sudo update-ca-certificates\n' "${CA_FILE}"
    printf 'RHEL/Fedora: sudo cp %q /etc/pki/ca-trust/source/anchors/moox.crt && sudo update-ca-trust\n' "${CA_FILE}"
    printf 'curl --cacert %q https://HOST:9527/\n' "${CA_FILE}"
    printf 'Applications: set MOOX_SERVICE_GATEWAY_CA_FILE=%q; never disable TLS verification.\n' "${CA_FILE}";;
  *) echo 'command required: fetch|inspect|install|status|trust-help' >&2; exit 2;;
esac
