#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
if [[ "$(basename "${SCRIPT_DIR}")" == "contract" ]]; then
  ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd -P)"
else
  ROOT="$(cd "${SCRIPT_DIR}/.." && pwd -P)"
fi
SCRIPT="${ROOT}/scripts/build-storage-linux.sh"
DEPLOY_SCRIPT="${ROOT}/scripts/deploy-moox.sh"
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/moox-build-storage-linux.XXXXXX")"
trap 'rm -rf "${TMP_ROOT}"' EXIT

command -v jq >/dev/null 2>&1 || { echo 'jq is required' >&2; exit 1; }
! grep -Fq '106.53.107.122' "${SCRIPT}"
grep -Fq '$(dirname "${BASH_SOURCE[0]}")/.."' "${SCRIPT}"
grep -Fq '"${ROOT}/scripts/build-storage-linux.sh"' "${DEPLOY_SCRIPT}"
grep -Fq '"${WITH_STORAGE}" -eq 1 || "${WITH_ADMIN}" -eq 1 || "${WITH_MONITOR}" -eq 1' "${DEPLOY_SCRIPT}"
grep -Fq '"${TARGET_GOARCH}" == amd64' "${DEPLOY_SCRIPT}"
grep -Fq 'TARGET_GOOS="${HOST_GOOS}" TARGET_GOARCH="${HOST_GOARCH}"' "${DEPLOY_SCRIPT}"
if grep -Fq '"${ROOT}/scripts/build.sh" all' "${DEPLOY_SCRIPT}"; then
  echo 'deploy-moox must build only enabled services and use the compile host for cross-platform Storage' >&2
  exit 1
fi

FAKE_BIN="${TMP_ROOT}/bin"
mkdir -p "${FAKE_BIN}"
: >"${TMP_ROOT}/known_hosts"
chmod 600 "${TMP_ROOT}/known_hosts"
printf '%s\n' 'placeholder' >"${ROOT}/custom.toml.contract-test"
chmod 600 "${ROOT}/custom.toml.contract-test"
trap 'rm -rf "${TMP_ROOT}"; rm -f "${ROOT}/custom.toml.contract-test"' EXIT

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
fi
EOF
chmod +x "${FAKE_BIN}/moox-cli" "${FAKE_BIN}/rsync" "${FAKE_BIN}/scp" "${FAKE_BIN}/ssh"

RSYNC_LOG="${TMP_ROOT}/rsync.log" \
SCP_LOG="${TMP_ROOT}/scp.log" \
SSH_LOG="${TMP_ROOT}/ssh.log" \
PATH="${FAKE_BIN}:${PATH}" \
MOOX_CLI="${FAKE_BIN}/moox-cli" \
CONFIG="${ROOT}/custom.toml.contract-test" \
KNOWN_HOSTS_PATH="${TMP_ROOT}/known_hosts" \
BIN_DIR="${TMP_ROOT}/output" \
LOCAL_BIN="${TMP_ROOT}/output" \
REMOTE_ROOT=/tmp/moox-build-contract \
GIT_COMMIT=test-sha \
VERSION=test-version \
MOOX_STORAGE_BUILD_HOST=storage \
bash "${SCRIPT}"

grep -Fq -- '--exclude=custom.toml' "${TMP_ROOT}/rsync.log"
grep -Fq -- '--exclude=bin' "${TMP_ROOT}/rsync.log"
grep -Fq -- '--exclude=release' "${TMP_ROOT}/rsync.log"
grep -Fq -- '--exclude=.worktrees' "${TMP_ROOT}/rsync.log"
grep -Fq -- '--exclude=node_modules' "${TMP_ROOT}/rsync.log"
grep -Fq -- '-p 2200' "${TMP_ROOT}/rsync.log"
grep -Fq -- '192.0.2.88' "${TMP_ROOT}/rsync.log"
grep -Fq -- 'StrictHostKeyChecking=yes' "${TMP_ROOT}/rsync.log"
grep -Fq -- 'UserKnownHostsFile' "${TMP_ROOT}/rsync.log"
grep -Fq -- 'UserKnownHostsFile' "${TMP_ROOT}/scp.log"
grep -Fq -- '-p' "${TMP_ROOT}/ssh.log"
grep -Fq -- '2200' "${TMP_ROOT}/ssh.log"
grep -Fq -- 'GIT_COMMIT=' "${TMP_ROOT}/ssh.log"
for binary in moox-storage-primary moox-storage-node moox-storage-view moox-storage-cli; do
  test -s "${TMP_ROOT}/output/${binary}"
done

echo 'build-storage-linux contract passed'
