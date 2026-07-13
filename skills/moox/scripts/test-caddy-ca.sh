#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
SCRIPT="${ROOT}/skills/moox/scripts/caddy-ca.sh"
TMP=$(mktemp -d "${TMPDIR:-/tmp}/moox-skill-ca.XXXXXX")
trap 'rm -rf "${TMP}"' EXIT
mkdir -p "${TMP}/bin" "${TMP}/CA path with 'quote'"
CA_FILE="${TMP}/CA path with 'quote'/root ca.crt"
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj '/CN=MooX Skill Root' \
  -addext 'basicConstraints=critical,CA:TRUE' -keyout "${TMP}/root.key" -out "${CA_FILE}" >/dev/null 2>&1
FINGERPRINT=$(openssl x509 -in "${CA_FILE}" -noout -fingerprint -sha256 | cut -d= -f2)
FINGERPRINT_HEX=${FINGERPRINT//:/}

cat >"${TMP}/bin/uname" <<'EOF'
#!/usr/bin/env bash
printf 'Darwin\n'
EOF
cat >"${TMP}/bin/security" <<'EOF'
#!/usr/bin/env bash
printf 'security' >>"${MOCK_LOG:-/dev/null}"; printf ' <%s>' "$@" >>"${MOCK_LOG:-/dev/null}"; printf '\n' >>"${MOCK_LOG:-/dev/null}"
[[ "${MOCK_TRUSTED:-0}" != 1 ]] || printf 'SHA-256 hash: %s\n' "${MOCK_FINGERPRINT_HEX}"
EOF
cat >"${TMP}/bin/sudo" <<'EOF'
#!/usr/bin/env bash
if [[ "${1:-}" == -n ]]; then
  shift
  if [[ "${1:-}" == -l ]]; then
    shift
    [[ "${1:-}" != -- ]] || shift
    [[ "${MOCK_SUDO_DENY:-0}" != 1 ]] || exit 1
    exit 0
  fi
  [[ "${MOCK_SUDO_DENY:-0}" != 1 ]] || exit 1
  [[ "${1:-}" != -- ]] || shift
fi
"$@"
EOF
cat >"${TMP}/bin/ssh" <<'EOF'
#!/usr/bin/env bash
while [[ $# -gt 0 ]]; do
  case "$1" in
    -o) shift 2 ;;
    -t) shift ;;
    *) shift; break ;;
  esac
done
[[ $# -eq 1 ]] || { echo "unexpected ssh arguments: $*" >&2; exit 2; }
bash -c "$1"
EOF
chmod +x "${TMP}/bin"/*

status() {
  PATH="${TMP}/bin:${PATH}" MOCK_TRUSTED=$1 MOCK_FINGERPRINT_HEX="${FINGERPRINT_HEX}" \
    "${SCRIPT}" status --ca-file "${CA_FILE}"
}

# RED: status reflects the platform trust store, not certificate validity alone.
grep -Fq '"trusted":false' < <(status 0)
grep -Fq '"trusted":true' < <(status 1)

inspect_out=$("${SCRIPT}" inspect --ca-file "${CA_FILE}")
grep -Eiq 'sha256 fingerprint=' <<<"${inspect_out}"
help_out=$("${SCRIPT}" trust-help --ca-file "${CA_FILE}")
grep -Fq 'curl --cacert' <<<"${help_out}"
if "${SCRIPT}" fetch --target localhost --deploy-dir "${TMP}" --output "${TMP}/root.key" >/dev/null 2>&1; then
  echo 'FAIL: private-key output path accepted' >&2; exit 1
fi
mkdir -p "${TMP}/deploy/certs/caddy"
cp "${CA_FILE}" "${TMP}/deploy/certs/caddy/root.crt"
: >"${TMP}/install.log"
PATH="${TMP}/bin:${PATH}" MOCK_TRUSTED=0 MOCK_FINGERPRINT_HEX="${FINGERPRINT_HEX}" MOCK_LOG="${TMP}/install.log" \
  "${SCRIPT}" install-target --target localhost --deploy-dir "${TMP}/deploy" >/dev/null
grep -Fq 'add-trusted-cert' "${TMP}/install.log"
set +e
PATH="${TMP}/bin:${PATH}" MOCK_TRUSTED=0 MOCK_SUDO_DENY=1 MOCK_FINGERPRINT_HEX="${FINGERPRINT_HEX}" MOCK_LOG="${TMP}/install.log" \
  "${SCRIPT}" install-target --target localhost --deploy-dir "${TMP}/deploy" --non-interactive >/dev/null 2>&1
status=$?
set -e
[[ "${status}" -eq 77 ]] || { echo "FAIL: permission denial returned ${status}, want 77" >&2; exit 1; }

REMOTE_HOME="${TMP}/remote-home"
REMOTE_DEPLOY="${REMOTE_HOME}/target path"
mkdir -p "${REMOTE_DEPLOY}/certs/caddy" "${REMOTE_DEPLOY}/lib"
cp "${CA_FILE}" "${REMOTE_DEPLOY}/certs/caddy/root.crt"
cp "${ROOT}/scripts/install-caddy-ca.sh" "${REMOTE_DEPLOY}/lib/install-caddy-ca.sh"
chmod +x "${REMOTE_DEPLOY}/lib/install-caddy-ca.sh"
: >"${TMP}/remote-install.log"
HOME="${REMOTE_HOME}" PATH="${TMP}/bin:${PATH}" MOCK_TRUSTED=0 MOCK_FINGERPRINT_HEX="${FINGERPRINT_HEX}" MOCK_LOG="${TMP}/remote-install.log" \
  "${SCRIPT}" install-target --target user@example --deploy-dir '~/target path' --non-interactive >/dev/null
grep -Fq 'add-trusted-cert' "${TMP}/remote-install.log"
printf 'PASS: skill status, target install, and CA safety contracts\n'
