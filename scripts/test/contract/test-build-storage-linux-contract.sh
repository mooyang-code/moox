#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)"
SCRIPT="${ROOT}/scripts/build/build-storage-linux.sh"
DEPLOY_SCRIPT="${ROOT}/scripts/deploy/deploy-moox.sh"
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/moox-build-storage-linux.XXXXXX")"
trap 'rm -rf "${TMP_ROOT}"' EXIT

command -v jq >/dev/null 2>&1 || { echo 'jq is required' >&2; exit 1; }
! grep -Fq '106.53.107.122' "${SCRIPT}"
grep -Fq '$(dirname "${BASH_SOURCE[0]}")/../.."' "${SCRIPT}"
grep -Fq '"${ROOT}/scripts/build/build-storage-linux.sh"' "${DEPLOY_SCRIPT}"
grep -Fq '"${WITH_STORAGE}" -eq 1 || "${WITH_ADMIN}" -eq 1 || "${WITH_MONITOR}" -eq 1' "${DEPLOY_SCRIPT}"
grep -Fq '"${TARGET_GOARCH}" == amd64' "${DEPLOY_SCRIPT}"
grep -Fq 'TARGET_GOOS="${HOST_GOOS}" TARGET_GOARCH="${HOST_GOARCH}"' "${DEPLOY_SCRIPT}"
if grep -Fq '"${ROOT}/scripts/build/build.sh" all' "${DEPLOY_SCRIPT}"; then
  echo 'deploy-moox must build only enabled services and use the compile host for cross-platform Storage' >&2
  exit 1
fi

FAKE_BIN="${TMP_ROOT}/bin"
mkdir -p "${FAKE_BIN}"
: >"${TMP_ROOT}/known_hosts"
chmod 600 "${TMP_ROOT}/known_hosts"
printf '%s\n' 'placeholder' >"${ROOT}/moox.toml.contract-test"
chmod 600 "${ROOT}/moox.toml.contract-test"
trap 'rm -rf "${TMP_ROOT}"; rm -f "${ROOT}/moox.toml.contract-test"' EXIT

cat >"${FAKE_BIN}/moox-cli" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' '{"hosts":[{"name":"storage","address":"192.0.2.88","port":2200,"username":"storage-builder","role":"other"},{"name":"compile","address":"192.0.2.77","port":2222,"username":"builder","role":"compile"}]}'
EOF
cat >"${FAKE_BIN}/rsync" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$@" >>"${RSYNC_LOG}"
if [[ "${!#}" == "${LOCAL_BIN}/" ]]; then
  mkdir -p "${LOCAL_BIN}"
  for binary in moox-storage-primary moox-storage-node moox-storage-view moox-storage-cli; do
    printf '%s\n' binary >"${LOCAL_BIN}/${binary}"
  done
fi
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
MOOX_STORAGE_BUILD_GOARCH=amd64 \
CONFIG="${ROOT}/moox.toml.contract-test" \
KNOWN_HOSTS_PATH="${TMP_ROOT}/known_hosts" \
BIN_DIR="${TMP_ROOT}/output" \
LOCAL_BIN="${TMP_ROOT}/output" \
REMOTE_ROOT=/tmp/moox-build-contract \
GIT_COMMIT=test-sha \
VERSION=test-version \
MOOX_STORAGE_BUILD_HOST=storage \
bash "${SCRIPT}"

grep -Fq -- '--exclude=moox.toml' "${TMP_ROOT}/rsync.log"
grep -Fq -- '--exclude=bin' "${TMP_ROOT}/rsync.log"
grep -Fq -- '--exclude=release' "${TMP_ROOT}/rsync.log"
grep -Fq -- '--exclude=.worktrees' "${TMP_ROOT}/rsync.log"
grep -Fq -- '--exclude=node_modules' "${TMP_ROOT}/rsync.log"
grep -Fq -- '-p 2200' "${TMP_ROOT}/rsync.log"
grep -Fq -- '192.0.2.88' "${TMP_ROOT}/rsync.log"
grep -Fq -- 'StrictHostKeyChecking=yes' "${TMP_ROOT}/rsync.log"
grep -Fq -- 'BatchMode=no' "${TMP_ROOT}/rsync.log"
grep -Fq -- 'UserKnownHostsFile' "${TMP_ROOT}/rsync.log"
grep -Fq -- 'UserKnownHostsFile' "${TMP_ROOT}/scp.log"
grep -Fq -- 'BatchMode=no' "${TMP_ROOT}/scp.log"
grep -Fq -- '-p' "${TMP_ROOT}/ssh.log"
grep -Fq -- '2200' "${TMP_ROOT}/ssh.log"
grep -Fq -- 'BatchMode=no' "${TMP_ROOT}/ssh.log"
grep -Fq -- 'GIT_COMMIT=' "${TMP_ROOT}/ssh.log"
grep -Fq -- 'TARGET_GOARCH=' "${TMP_ROOT}/ssh.log"
! grep -Fq -- 'fixture-password' "${TMP_ROOT}/rsync.log" "${TMP_ROOT}/scp.log" "${TMP_ROOT}/ssh.log"
for binary in moox-storage-primary moox-storage-node moox-storage-view moox-storage-cli; do
  test -s "${TMP_ROOT}/output/${binary}"
done

echo 'build-storage-linux contract passed'
