#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
SCRIPT="${ROOT}/scripts/install-caddy-ca.sh"
TMP=$(mktemp -d "${TMPDIR:-/tmp}/moox-ca-install.XXXXXX")
trap 'rm -rf "${TMP}"' EXIT

CA_DIR="${TMP}/CA files with spaces and 'quote'"
BIN="${TMP}/bin"
mkdir -p "${CA_DIR}" "${BIN}"
CA_FILE="${CA_DIR}/root 'authority'.crt"
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj '/CN=MooX Test Root' \
  -addext 'basicConstraints=critical,CA:TRUE' -keyout "${TMP}/root.key" -out "${CA_FILE}" >/dev/null 2>&1
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj '/CN=Unrelated Test Root' \
  -addext 'basicConstraints=critical,CA:TRUE' -keyout "${TMP}/other.key" -out "${TMP}/other.crt" >/dev/null 2>&1
FINGERPRINT=$(openssl x509 -in "${CA_FILE}" -noout -fingerprint -sha256 | cut -d= -f2)
FINGERPRINT_HEX=${FINGERPRINT//:/}

cat >"${BIN}/uname" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "${MOCK_UNAME}"
EOF
cat >"${BIN}/sudo" <<'EOF'
#!/usr/bin/env bash
printf 'sudo' >>"${MOCK_LOG}"; printf ' <%s>' "$@" >>"${MOCK_LOG}"; printf '\n' >>"${MOCK_LOG}"
if [[ "${1:-}" == -n ]]; then
  shift
  if [[ "${1:-}" == -l ]]; then
    shift
    [[ "${1:-}" != -- ]] || shift
    [[ "${1:-}" != "${MOCK_SUDO_DENY_COMMAND:-}" ]] || exit 1
    exit 0
  fi
  [[ "${1:-}" != -- ]] || shift
fi
"$@"
EOF
cat >"${BIN}/security" <<'EOF'
#!/usr/bin/env bash
printf 'security' >>"${MOCK_LOG}"; printf ' <%s>' "$@" >>"${MOCK_LOG}"; printf '\n' >>"${MOCK_LOG}"
[[ "${1:-}" != find-certificate || "${MOCK_TRUSTED:-0}" != 1 ]] || printf 'SHA-256 hash: %s\n' "${MOCK_FINGERPRINT_HEX}"
EOF
cat >"${BIN}/powershell.exe" <<'EOF'
#!/usr/bin/env bash
printf 'powershell' >>"${MOCK_LOG}"; printf ' <%s>' "$@" >>"${MOCK_LOG}"; printf '\n' >>"${MOCK_LOG}"
[[ "${MOCK_TRUSTED:-0}" != 1 ]] || printf '%s\n' "${MOCK_FINGERPRINT_HEX}"
EOF
cat >"${BIN}/update-ca-certificates" <<'EOF'
#!/usr/bin/env bash
printf 'update-ca-certificates\n' >>"${MOCK_LOG}"
[[ "${MOCK_UPDATE_FAIL:-0}" != 1 ]] || exit 1
mkdir -p "${MOCK_SYSTEM_DIR}"
cp "${MOOX_CA_DEBIAN_DIR}"/*.crt "${MOCK_SYSTEM_DIR}/"
cat "${MOCK_OTHER_CA}" "${MOOX_CA_DEBIAN_DIR}"/*.crt >"${MOCK_SYSTEM_BUNDLE}"
EOF
cat >"${BIN}/update-ca-trust" <<'EOF'
#!/usr/bin/env bash
printf 'update-ca-trust\n' >>"${MOCK_LOG}"
EOF
chmod +x "${BIN}"/*

run_mock() {
  local platform=$1 trusted=$2 log=$3; shift 3
  PATH="${BIN}:${PATH}" MOCK_UNAME="${platform}" MOCK_TRUSTED="${trusted}" \
    MOCK_FINGERPRINT_HEX="${FINGERPRINT_HEX}" MOCK_LOG="${log}" \
    MOCK_SYSTEM_DIR="${TMP}/system" MOCK_SYSTEM_BUNDLE="${TMP}/system-bundle.pem" MOCK_OTHER_CA="${TMP}/other.crt" \
    MOOX_CA_SYSTEM_DIR="${TMP}/system" MOOX_CA_SYSTEM_BUNDLE="${TMP}/system-bundle.pem" \
    MOOX_CA_DEBIAN_DIR="${TMP}/debian" MOOX_CA_RHEL_DIR="${TMP}/rhel" \
    "${SCRIPT}" --ca-file "${CA_FILE}" "$@"
}

# RED: an existing macOS trust anchor must not be imported again.
: >"${TMP}/mac.log"
run_mock Darwin 1 "${TMP}/mac.log" >"${TMP}/mac.out"
grep -Fq 'already trusted' "${TMP}/mac.out"
! grep -Fq 'add-trusted-cert' "${TMP}/mac.log"

# RED: Linux installs once by fingerprint, then becomes idempotent.
mkdir -p "${TMP}/debian"
: >"${TMP}/linux.log"
run_mock Linux 0 "${TMP}/linux.log" >/dev/null
run_mock Linux 0 "${TMP}/linux.log" >"${TMP}/linux-two.out"
grep -Fq 'already trusted' "${TMP}/linux-two.out"
[[ $(grep -c '^update-ca-certificates$' "${TMP}/linux.log") -eq 1 ]]

# A failed trust database update must remove the copied anchor so a retry can run.
rm -f "${TMP}/debian"/*.crt
rm -f "${TMP}/system"/*.crt
rm -f "${TMP}/system-bundle.pem"
: >"${TMP}/linux-fail.log"
if MOCK_UPDATE_FAIL=1 run_mock Linux 0 "${TMP}/linux-fail.log" >/dev/null 2>&1; then
  echo 'FAIL: failed trust database update was accepted' >&2; exit 1
fi
[[ -z $(find "${TMP}/debian" -type f -print -quit) ]]
run_mock Linux 0 "${TMP}/linux-fail.log" >/dev/null
[[ $(grep -c '^update-ca-certificates$' "${TMP}/linux-fail.log") -eq 2 ]]

# A stale source anchor alone is not proof that the system trust store was updated.
rm -f "${TMP}/system"/*.crt
rm -f "${TMP}/system-bundle.pem"
: >"${TMP}/linux-stale.log"
run_mock Linux 0 "${TMP}/linux-stale.log" >/dev/null
grep -Fq 'update-ca-certificates' "${TMP}/linux-stale.log"

# Auto installation distinguishes unavailable privilege escalation from trust/update failures.
NO_SUDO_BIN="${TMP}/no-sudo-bin"
mkdir -p "${NO_SUDO_BIN}"
for command in bash cut find grep mkdir openssl uname; do
  ln -s "$(command -v "${command}")" "${NO_SUDO_BIN}/${command}"
done
ln -s "${BIN}/update-ca-certificates" "${NO_SUDO_BIN}/update-ca-certificates"
set +e
PATH="${NO_SUDO_BIN}" MOCK_UNAME=Linux MOCK_LOG="${TMP}/no-sudo.log" \
  MOOX_CA_SUDO_NONINTERACTIVE=1 MOOX_CA_SYSTEM_DIR="${TMP}/no-sudo-system" \
  MOOX_CA_DEBIAN_DIR="${TMP}/no-sudo-anchor" \
  "${SCRIPT}" --ca-file "${CA_FILE}" >/dev/null 2>&1
no_sudo_status=$?
set -e
[[ ${no_sudo_status} -eq 77 ]] || { echo "FAIL: no-sudo status was ${no_sudo_status}, want 77" >&2; exit 1; }

rm -f "${TMP}/debian"/*.crt "${TMP}/system"/*.crt "${TMP}/system-bundle.pem"
set +e
MOOX_CA_SUDO_NONINTERACTIVE=1 MOCK_SUDO_DENY_COMMAND=update-ca-certificates \
  run_mock Linux 0 "${TMP}/command-denied.log" >/dev/null 2>&1
command_denied_status=$?
set -e
[[ ${command_denied_status} -eq 77 ]] || { echo "FAIL: command-level sudo denial returned ${command_denied_status}, want 77" >&2; exit 1; }

# RED: dry-run reports the privileged operation but changes nothing.
rm -f "${TMP}/debian"/*.crt
rm -f "${TMP}/system"/*.crt
rm -f "${TMP}/system-bundle.pem"
: >"${TMP}/dry.log"
run_mock Linux 0 "${TMP}/dry.log" --dry-run >"${TMP}/dry.out"
grep -Fq 'DRY-RUN:' "${TMP}/dry.out"
[[ -z $(find "${TMP}/debian" -type f -print -quit) ]]

# RED: Windows passes the quoted path as one argument instead of embedding it in code.
: >"${TMP}/windows.log"
run_mock MINGW64_NT-10.0 0 "${TMP}/windows.log" >/dev/null
grep -Fq "<${CA_FILE}>" "${TMP}/windows.log"
! grep -Fq -- "-LiteralPath '${CA_FILE}'" "${TMP}/windows.log"

if "${SCRIPT}" --ca-file "${TMP}/root.key" --dry-run >/dev/null 2>&1; then
  echo 'FAIL: private key accepted' >&2; exit 1
fi
printf 'PASS: fingerprint idempotency, platform stores, dry-run, and safe paths\n'
