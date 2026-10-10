#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CONFIG="${CONFIG:-${ROOT}/moox.toml}"
CONFIG_ROOT="${MOOX_CONFIG_ROOT:-${ROOT}}"
REMOTE_ROOT="${REMOTE_ROOT:-moox-build}"
MOOX_CLI="${MOOX_CLI:-${ROOT}/bin/moox-cli}"
BIN_DIR="${BIN_DIR:-${ROOT}/bin}"
KNOWN_HOSTS_PATH="${KNOWN_HOSTS_PATH:-${HOME}/.config/moox/known_hosts}"

die() {
  echo "build-storage-linux: $*" >&2
  exit 1
}

linux_cgo_target="${MOOX_LINUX_CGO_TARGET:-storage}"
case "${linux_cgo_target}" in
  storage)
    linux_cgo_binaries=(moox-storage-primary moox-storage-node moox-storage-view)
    ;;
  storage-primary)
    linux_cgo_binaries=(moox-storage-primary)
    ;;
  *)
    die "unsupported linux CGO build target: ${linux_cgo_target}"
    ;;
esac

build_password="${MOOX_SSH_PASSWORD:-}"
unset MOOX_SSH_PASSWORD

shell_quote() {
  printf "'%s'" "${1//\'/\'\\\'\'}"
}

[[ -f "${CONFIG}" ]] || die "missing repository moox.toml"
[[ -x "${MOOX_CLI}" ]] || die "missing executable moox-cli: ${MOOX_CLI}"
[[ -f "${KNOWN_HOSTS_PATH}" ]] || die "missing trusted SSH host-key store: ${KNOWN_HOSTS_PATH}"
command -v jq >/dev/null 2>&1 || die "jq is required to read sanitized setup host output"
command -v rsync >/dev/null 2>&1 || die "rsync is required"
command -v scp >/dev/null 2>&1 || die "scp is required"
command -v ssh >/dev/null 2>&1 || die "ssh is required"

hosts_json="$(cd "${CONFIG_ROOT}" && "${MOOX_CLI}" setup hosts --file "${CONFIG}")" || die "unable to load build host through moox-cli"
build_host_name="${MOOX_STORAGE_BUILD_HOST:-}"
build_host_role="${MOOX_STORAGE_BUILD_HOST_ROLE:-}"
if [[ -n "${build_host_name}" ]]; then
  build_host_json="$(jq -cer --arg name "${build_host_name}" --arg role "${build_host_role}" '[.hosts[] | select(.name == $name) | select($role == "" or .role == $role)] | if length == 1 then .[0] else error("storage build host must match exactly one configured host") end' <<<"${hosts_json}")" || die "storage build host is missing or invalid"
elif [[ -n "${build_host_role}" ]]; then
  build_host_json="$(jq -cer --arg role "${build_host_role}" '[.hosts[] | select(.role == $role)] | if length == 1 then .[0] else error("storage build host role must match exactly one configured host") end' <<<"${hosts_json}")" || die "storage build host role is missing or invalid"
else
  build_host_json="$(jq -cer '[.hosts[] | select(.role == "compile")] | if length == 1 then .[0] else error("compile_host must be configured exactly once") end' <<<"${hosts_json}")" || die "compile_host is missing or invalid"
fi

address="$(jq -er '.address' <<<"${build_host_json}")" || die "storage build host.address is missing"
port="$(jq -er '.port' <<<"${build_host_json}")" || die "storage build host.port is missing"
username="$(jq -er '.username' <<<"${build_host_json}")" || die "storage build host.username is missing"
name="$(jq -er '.name' <<<"${build_host_json}")" || die "storage build host.name is missing"

[[ "${port}" =~ ^[0-9]+$ && "${port}" -ge 1 && "${port}" -le 65535 ]] || die "storage build host.port is invalid"

git_commit="${GIT_COMMIT:-$(git -C "${ROOT}" rev-parse HEAD 2>/dev/null || echo unknown)}"
version="${VERSION:-${git_commit}}"
remote="${username}@${address}"
remote_root_q="$(shell_quote "${REMOTE_ROOT}")"
version_q="$(shell_quote "${version}")"
git_commit_q="$(shell_quote "${git_commit}")"
known_hosts_q="$(shell_quote "${KNOWN_HOSTS_PATH}")"
target_goarch="${MOOX_STORAGE_BUILD_GOARCH:-amd64}"
case "${target_goarch}" in
  amd64|arm64) ;;
  *) die "unsupported storage build architecture: ${target_goarch}" ;;
esac
target_goarch_q="$(shell_quote "${target_goarch}")"
remote_build_target="${linux_cgo_target}"
[[ "${linux_cgo_target}" != storage ]] || remote_build_target=storage-cgo
remote_build_target_q="$(shell_quote "${remote_build_target}")"

rsync_ssh="ssh -o BatchMode=yes -o StrictHostKeyChecking=yes -o ConnectTimeout=15 -o ServerAliveInterval=15 -o ServerAliveCountMax=2 -o UserKnownHostsFile=${known_hosts_q}"
ssh_args=(ssh -o BatchMode=yes -o StrictHostKeyChecking=yes -o ConnectTimeout=15 -o ServerAliveInterval=15 -o ServerAliveCountMax=2 -o "UserKnownHostsFile=${KNOWN_HOSTS_PATH}")
# Use the legacy scp protocol for large immutable binaries.  The default
# SFTP-backed scp intermittently stalls on this build host; compression keeps
# the transfer small without changing the produced artifacts.
scp_args=(scp -O -C -o BatchMode=yes -o StrictHostKeyChecking=yes -o ConnectTimeout=15 -o ServerAliveInterval=15 -o ServerAliveCountMax=2 -o "UserKnownHostsFile=${KNOWN_HOSTS_PATH}")
# Prefer the operator's working key/agent even when a manifest contains a
# password. Only pass the password to sshpass if key authentication fails.
key_probe=("${ssh_args[@]}")
[[ "${port}" == 22 ]] || key_probe+=(-p "${port}")
if [[ -n "${build_password}" ]] && "${key_probe[@]}" "${remote}" true >/dev/null 2>&1; then
  build_password=''
fi
if [[ -n "${build_password}" ]]; then
  command -v sshpass >/dev/null 2>&1 || die "sshpass is required for password-authenticated storage build hosts"
  rsync_ssh="sshpass -e ssh -o BatchMode=no -o PreferredAuthentications=password -o PubkeyAuthentication=no -o StrictHostKeyChecking=yes -o ConnectTimeout=15 -o ServerAliveInterval=15 -o ServerAliveCountMax=2 -o UserKnownHostsFile=${known_hosts_q}"
  ssh_args=(sshpass -e ssh -o BatchMode=no -o PreferredAuthentications=password -o PubkeyAuthentication=no -o StrictHostKeyChecking=yes -o ConnectTimeout=15 -o ServerAliveInterval=15 -o ServerAliveCountMax=2 -o "UserKnownHostsFile=${KNOWN_HOSTS_PATH}")
  scp_args=(sshpass -e scp -O -C -o BatchMode=no -o PreferredAuthentications=password -o PubkeyAuthentication=no -o StrictHostKeyChecking=yes -o ConnectTimeout=15 -o ServerAliveInterval=15 -o ServerAliveCountMax=2 -o "UserKnownHostsFile=${KNOWN_HOSTS_PATH}")
fi
if [[ "${port}" != "22" ]]; then
  rsync_ssh+=" -p ${port}"
  ssh_args+=(-p "${port}")
  scp_args+=(-P "${port}")
fi

run_rsync() {
  if [[ -n "${build_password}" ]]; then
    (export SSHPASS="${build_password}"; command rsync "$@")
    return
  fi
  command rsync "$@"
}

run_ssh() {
  if [[ -n "${build_password}" ]]; then
    (export SSHPASS="${build_password}"; "${ssh_args[@]}" "$@")
    return
  fi
  "${ssh_args[@]}" "$@"
}

run_scp() {
  if [[ -n "${build_password}" ]]; then
    (export SSHPASS="${build_password}"; "${scp_args[@]}" "$@")
    return
  fi
  "${scp_args[@]}" "$@"
}

local_sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
    return
  fi
  shasum -a 256 "$1" | awk '{print $1}'
}

local_file_size() {
  if stat -c '%s' "$1" 2>/dev/null; then
    return
  fi
  stat -f '%z' "$1"
}

echo "==> sync source to Storage build host ${name} (${username}@${address}:${port})"
run_rsync -az --delete \
  --exclude='.git' \
  --exclude='moox.toml' \
  --exclude='bin' \
  --exclude='release' \
  --exclude='artifacts' \
  --exclude='.worktrees' \
  --exclude='.tmp' \
  --exclude='.codex' \
  --exclude='.superpowers' \
  --exclude='node_modules' \
  --exclude='web/node_modules' \
  --exclude='web/dist' \
  -e "${rsync_ssh}" \
  "${ROOT}/" "${remote}:${REMOTE_ROOT}/"

echo "==> build ${linux_cgo_target} on ${name} (${version})"
run_ssh "${remote}" \
  "set -eu; cd ${remote_root_q}; required=\$(tr -d '[:space:]' < .go-version); if [ -x \"\$HOME/.local/go\$required/bin/go\" ]; then export PATH=\"\$HOME/.local/go\$required/bin:\$PATH\"; fi; export GOTOOLCHAIN=local; bash ./scripts/ci/check-go-version.sh; command -v gcc >/dev/null; command -v g++ >/dev/null; mkdir -p bin .gotmp; GOTMPDIR=\"\$PWD/.gotmp\" GOFLAGS=-buildvcs=false VERSION=${version_q} GIT_COMMIT=${git_commit_q} CGO_ENABLED=1 TARGET_GOOS=linux TARGET_GOARCH=${target_goarch_q} bash ./scripts/build/build.sh ${remote_build_target_q}"

mkdir -p "${BIN_DIR}"
echo "==> download Linux ${linux_cgo_target} binaries from ${name}"
for binary in "${linux_cgo_binaries[@]}"; do
  # Use scp for the large remote artifacts. The rsync daemon on some build
  # hosts leaves a remote single-file pull open after the payload is complete;
  # scp closes the SSH channel reliably and is sufficient for these immutable
  # release binaries.
  remote_binary="${REMOTE_ROOT}/bin/${binary}"
  remote_binary_q="$(shell_quote "${remote_binary}")"
  remote_size="$(run_ssh "${remote}" "stat -c %s ${remote_binary_q}")"
  [[ "${remote_size}" =~ ^[0-9]+$ && "${remote_size}" -gt 0 ]] || die "remote build returned invalid size for ${binary}"
  remote_sha256="$(run_ssh "${remote}" "sha256sum ${remote_binary_q} | awk '{print \$1}'")"
  [[ "${remote_sha256}" =~ ^[0-9a-fA-F]{64}$ ]] || die "remote build returned invalid checksum for ${binary}"
  local_final="${BIN_DIR}/${binary}"
  if [[ -s "${local_final}" ]]; then
    local_size="$(local_file_size "${local_final}" 2>/dev/null || true)"
    local_sha256_value="$(local_sha256 "${local_final}" 2>/dev/null || true)"
    if [[ "${local_size}" == "${remote_size}" && "${local_sha256_value}" == "${remote_sha256}" ]]; then
      echo "==> reuse verified Linux ${binary}"
      continue
    fi
  fi
  local_tmp="${BIN_DIR}/.${binary}.tmp"
  rm -f "${local_tmp}"
  run_scp "${remote}:${remote_binary}" "${local_tmp}" &
  copy_pid=$!
  copy_done=0
  for _ in $(seq 1 900); do
    if [[ -s "${local_tmp}" ]]; then
      local_size="$(local_file_size "${local_tmp}" 2>/dev/null || true)"
      if [[ "${local_size}" == "${remote_size}" ]]; then
        copy_done=1
        kill "${copy_pid}" 2>/dev/null || true
        wait "${copy_pid}" 2>/dev/null || true
        break
      fi
    fi
    if ! kill -0 "${copy_pid}" 2>/dev/null; then
      wait "${copy_pid}" || die "download failed for ${binary}"
      break
    fi
    sleep 1
  done
  [[ "${copy_done}" -eq 1 ]] || die "timed out downloading ${binary}"
  local_sha256_value="$(local_sha256 "${local_tmp}")"
  [[ "${local_sha256_value}" == "${remote_sha256}" ]] || die "checksum mismatch for ${binary}"
  mv "${local_tmp}" "${BIN_DIR}/${binary}"
done

if [[ "${linux_cgo_target}" == storage ]]; then
  echo '==> build non-CGO Storage CLI locally'
  BIN_DIR="${BIN_DIR}" VERSION="${version}" GIT_COMMIT="${git_commit}" TARGET_GOOS=linux TARGET_GOARCH="${target_goarch}" \
    bash "${ROOT}/scripts/build/build.sh" storage-cli
  echo '==> build non-CGO Access locally'
  BIN_DIR="${BIN_DIR}" VERSION="${version}" GIT_COMMIT="${git_commit}" TARGET_GOOS=linux TARGET_GOARCH="${target_goarch}" \
    bash "${ROOT}/scripts/build/build.sh" access
fi

for binary in "${linux_cgo_binaries[@]}"; do
  [[ -s "${BIN_DIR}/${binary}" ]] || die "remote build did not return ${binary}"
done

echo "==> Linux CGO ${linux_cgo_target} service binaries downloaded from ${name} to ${BIN_DIR}; pure Go tools are built locally"
