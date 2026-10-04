#!/usr/bin/env bash
set -euo pipefail

CA_FILE= DRY_RUN=0 CHECK_ONLY=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --ca-file) CA_FILE=${2:?}; shift 2;;
    --dry-run) DRY_RUN=1; shift;;
    --check) CHECK_ONLY=1; shift;;
    *) echo "unknown option: $1" >&2; exit 2;;
  esac
done

[[ -f "${CA_FILE}" ]] || { echo 'CA certificate file is required' >&2; exit 1; }
grep -q -- 'PRIVATE KEY' "${CA_FILE}" && { echo 'private keys are forbidden' >&2; exit 1; }
openssl x509 -in "${CA_FILE}" -noout -text | grep -Eq 'CA:TRUE' || { echo 'certificate is not a CA' >&2; exit 1; }
fingerprint=$(openssl x509 -in "${CA_FILE}" -noout -fingerprint -sha256 | cut -d= -f2)
fingerprint_hex=${fingerprint//:/}
platform=$(uname -s)

run() {
  if [[ "${DRY_RUN}" == 1 ]]; then
    printf 'DRY-RUN:'; printf ' %q' "$@"; printf '\n'
  else
    "$@"
  fi
}

run_privileged() {
  if [[ ${EUID} -eq 0 ]]; then
    run "$@"
  elif command -v sudo >/dev/null 2>&1; then
    if [[ "${MOOX_CA_SUDO_NONINTERACTIVE:-0}" == 1 ]]; then
      if ! sudo -n -l -- "$@" >/dev/null 2>&1; then
        echo "permission denied: passwordless sudo permission for the requested CA operation is required" >&2
        return 77
      fi
      run sudo -n -- "$@"
    else
      run sudo "$@"
    fi
  else
    echo "permission denied: root privileges are required to run $1" >&2
    [[ "${MOOX_CA_SUDO_NONINTERACTIVE:-0}" != 1 ]] || return 77
    return 1
  fi
}

ca_path_verifies() {
  local directory=$1
  [[ -d "${directory}" ]] && openssl verify -CApath "${directory}" "${CA_FILE}" >/dev/null 2>&1
}

ca_bundle_verifies() {
  local bundle=$1
  [[ -f "${bundle}" ]] && openssl verify -CAfile "${bundle}" "${CA_FILE}" >/dev/null 2>&1
}

is_trusted() {
  case "${platform}" in
    Darwin)
      # Chrome/Safari's normal macOS trust path is the system keychain. A
      # certificate merely imported into the login keychain can still produce
      # ERR_CERT_AUTHORITY_INVALID, so do not treat presence there as proof of
      # browser trust.
      security find-certificate -a -Z /Library/Keychains/System.keychain 2>/dev/null |
        grep -Eiq "SHA-256 hash:[[:space:]]*${fingerprint_hex}"
      ;;
    Linux)
      ca_path_verifies "${MOOX_CA_SYSTEM_DIR:-/etc/ssl/certs}" ||
        ca_bundle_verifies "${MOOX_CA_SYSTEM_BUNDLE:-/etc/ssl/certs/ca-certificates.crt}" ||
        ca_bundle_verifies /etc/pki/tls/certs/ca-bundle.crt ||
        ca_bundle_verifies /etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem
      ;;
    MINGW*|MSYS*|CYGWIN*)
      powershell.exe -NoProfile -NonInteractive -Command \
        '$wanted=$args[0]; Get-ChildItem Cert:\CurrentUser\Root,Cert:\LocalMachine\Root -ErrorAction SilentlyContinue | Where-Object { $_.Thumbprint -eq $wanted } | Select-Object -First 1 -ExpandProperty Thumbprint' \
        "${fingerprint_hex}" | grep -Eiq "^${fingerprint_hex}$"
      ;;
    *) return 1;;
  esac
}

if is_trusted; then
  [[ "${CHECK_ONLY}" == 1 ]] && exit 0
  printf 'SHA256 Fingerprint=%s\nCA is already trusted; no changes made.\n' "${fingerprint}"
  exit 0
fi
[[ "${CHECK_ONLY}" == 1 ]] && exit 1

printf 'SHA256 Fingerprint=%s\n' "${fingerprint}"
case "${platform}" in
  Darwin)
    run_privileged security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain "${CA_FILE}"
    ;;
  Linux)
    if command -v update-ca-certificates >/dev/null 2>&1; then
      dest="${MOOX_CA_DEBIAN_DIR:-/usr/local/share/ca-certificates}/moox-${fingerprint_hex}.crt"
      run_privileged mkdir -p "$(dirname "${dest}")"
      run_privileged cp "${CA_FILE}" "${dest}"
      if run_privileged update-ca-certificates; then
        :
      else
        status=$?
        run_privileged rm -f "${dest}" || true
        exit "${status}"
      fi
    elif command -v update-ca-trust >/dev/null 2>&1; then
      dest="${MOOX_CA_RHEL_DIR:-/etc/pki/ca-trust/source/anchors}/moox-${fingerprint_hex}.crt"
      run_privileged mkdir -p "$(dirname "${dest}")"
      run_privileged cp "${CA_FILE}" "${dest}"
      if run_privileged update-ca-trust; then
        :
      else
        status=$?
        run_privileged rm -f "${dest}" || true
        exit "${status}"
      fi
    else
      echo 'no supported Linux CA trust command found (update-ca-certificates or update-ca-trust)' >&2
      exit 1
    fi
    ;;
  MINGW*|MSYS*|CYGWIN*)
    run powershell.exe -NoProfile -NonInteractive -Command \
      'Import-Certificate -LiteralPath $args[0] -CertStoreLocation Cert:\CurrentUser\Root | Out-Null' \
      "${CA_FILE}"
    ;;
  *)
    printf 'unsupported platform %q; install %q in the operating system trust store\n' "${platform}" "${CA_FILE}" >&2
    exit 1
    ;;
esac
