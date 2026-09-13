#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)"
SCRIPT="${ROOT}/scripts/build/build-storage-linux.sh"
WRAPPER="${ROOT}/scripts/build/build-factor-linux.sh"
PACKAGER="${ROOT}/scripts/build/package-factor-engine.sh"
DEPLOY_SCRIPT="${ROOT}/scripts/deploy/deploy-moox.sh"
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/moox-build-factor-linux.XXXXXX")"
trap 'rm -rf "${TMP_ROOT}"' EXIT

command -v jq >/dev/null 2>&1 || { echo 'jq is required' >&2; exit 1; }
bash -n "${WRAPPER}"
grep -Fq 'MOOX_LINUX_CGO_TARGET="${MOOX_LINUX_CGO_TARGET:-factor}"' "${WRAPPER}"
grep -Fq 'build-storage-linux.sh' "${WRAPPER}"
grep -Fq 'MOOX_LINUX_CGO_TARGET=factor-engine' "${PACKAGER}"
grep -Fq 'MOOX_LINUX_CGO_TARGET=factor' "${DEPLOY_SCRIPT}"
grep -Fq 'moox-cli.host' "${DEPLOY_SCRIPT}"
grep -Fq 'MOOX_CLI="${host_cli}" MOOX_LINUX_CGO_TARGET=factor' "${DEPLOY_SCRIPT}"
grep -Fq 'linux_cgo_target="${MOOX_LINUX_CGO_TARGET:-storage}"' "${SCRIPT}"

FAKE_BIN="${TMP_ROOT}/bin"
mkdir -p "${FAKE_BIN}"
: >"${TMP_ROOT}/known_hosts"
chmod 600 "${TMP_ROOT}/known_hosts"
printf '%s\n' 'placeholder' >"${ROOT}/moox.toml.contract-test"
chmod 600 "${ROOT}/moox.toml.contract-test"
trap 'rm -rf "${TMP_ROOT}"; rm -f "${ROOT}/moox.toml.contract-test"' EXIT

cat >"${FAKE_BIN}/moox-cli" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' '{"hosts":[{"name":"compile","address":"192.0.2.77","port":2222,"username":"builder","role":"compile"}]}'
EOF
cat >"${FAKE_BIN}/rsync" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$@" >>"${RSYNC_LOG}"
EOF
cat >"${FAKE_BIN}/scp" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$@" >>"${SCP_LOG}"
destination="${!#}"
mkdir -p "$(dirname "${destination}")"
printf '%s\n' binary >"${destination}"
EOF
cat >"${FAKE_BIN}/ssh" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$@" >>"${SSH_LOG}"
if [[ "$*" == *"stat -c %s"* ]]; then
  printf '7\n'
elif [[ "$*" == *"sha256sum"* ]]; then
  printf '%s\n' "${REMOTE_SHA256}"
fi
EOF
cat >"${FAKE_BIN}/sshpass" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ "${1:-}" == -e ]] || exit 1
shift
exec "$@"
EOF
chmod +x "${FAKE_BIN}/moox-cli" "${FAKE_BIN}/rsync" "${FAKE_BIN}/scp" "${FAKE_BIN}/ssh" "${FAKE_BIN}/sshpass"

REMOTE_SHA256="$(printf '%s\n' binary | shasum -a 256 | awk '{print $1}')"
export REMOTE_SHA256

RSYNC_LOG="${TMP_ROOT}/rsync.log" \
SCP_LOG="${TMP_ROOT}/scp.log" \
SSH_LOG="${TMP_ROOT}/ssh.log" \
PATH="${FAKE_BIN}:${PATH}" \
MOOX_CLI="${FAKE_BIN}/moox-cli" \
MOOX_SSH_PASSWORD=fixture-password \
MOOX_LINUX_CGO_TARGET=factor-engine \
CONFIG="${ROOT}/moox.toml.contract-test" \
KNOWN_HOSTS_PATH="${TMP_ROOT}/known_hosts" \
BIN_DIR="${TMP_ROOT}/output" \
REMOTE_ROOT=/tmp/moox-build-contract \
GIT_COMMIT=test-sha \
VERSION=test-version \
bash "${SCRIPT}"

grep -Fq -- 'bash ./scripts/build/build.sh' "${TMP_ROOT}/ssh.log"
grep -Fq -- 'factor-engine' "${TMP_ROOT}/ssh.log"
! grep -Fq -- 'build.sh storage' "${TMP_ROOT}/ssh.log"
test -s "${TMP_ROOT}/output/moox-factor-engine"
! test -e "${TMP_ROOT}/output/moox-storage-primary"
! grep -Fq -- 'fixture-password' "${TMP_ROOT}/rsync.log" "${TMP_ROOT}/scp.log" "${TMP_ROOT}/ssh.log"

echo 'build-factor-linux contract passed'
