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
[[ "${MOCK_TRUSTED:-0}" != 1 ]] || printf 'SHA-256 hash: %s\n' "${MOCK_FINGERPRINT_HEX}"
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
printf 'PASS: skill status trust detection and CA safety contracts\n'
