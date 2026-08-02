#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CONFIG="${CONFIG:-${ROOT}/custom.toml}"
REMOTE_ROOT="${REMOTE_ROOT:-/tmp/moox-build}"
MOOX_CLI="${MOOX_CLI:-${ROOT}/bin/moox-cli}"
BIN_DIR="${BIN_DIR:-${ROOT}/bin}"
KNOWN_HOSTS_PATH="${KNOWN_HOSTS_PATH:-${HOME}/.config/moox/known_hosts}"

die() {
  echo "build-storage-linux: $*" >&2
  exit 1
}

shell_quote() {
  printf "'%s'" "${1//\'/\'\\\'\'}"
}

[[ -f "${CONFIG}" ]] || die "missing repository custom.toml"
[[ -x "${MOOX_CLI}" ]] || die "missing executable moox-cli: ${MOOX_CLI}"
[[ -f "${KNOWN_HOSTS_PATH}" ]] || die "missing trusted SSH host-key store: ${KNOWN_HOSTS_PATH}"
command -v jq >/dev/null 2>&1 || die "jq is required to read sanitized setup host output"
command -v rsync >/dev/null 2>&1 || die "rsync is required"
command -v ssh >/dev/null 2>&1 || die "ssh is required"

hosts_json="$(${MOOX_CLI} setup hosts --file "${CONFIG}")" || die "unable to load build host through moox-cli"
build_host_name="${MOOX_STORAGE_BUILD_HOST:-}"
if [[ -n "${build_host_name}" ]]; then
  build_host_json="$(jq -cer --arg name "${build_host_name}" '[.hosts[] | select(.name == $name)] | if length == 1 then .[0] else error("storage build host must match exactly one configured host") end' <<<"${hosts_json}")" || die "storage build host is missing or invalid"
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

rsync_ssh="ssh -o BatchMode=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=${known_hosts_q}"
ssh_args=(ssh -o BatchMode=yes -o StrictHostKeyChecking=yes -o "UserKnownHostsFile=${KNOWN_HOSTS_PATH}")
if [[ "${port}" != "22" ]]; then
  rsync_ssh+=" -p ${port}"
  ssh_args+=(-p "${port}")
fi

echo "==> sync source to Storage build host ${name} (${username}@${address}:${port})"
rsync -az --delete \
  --exclude='.git' \
  --exclude='custom.toml' \
  --exclude='bin' \
  --exclude='release' \
  --exclude='artifacts' \
  --exclude='web/node_modules' \
  --exclude='web/dist' \
  -e "${rsync_ssh}" \
  "${ROOT}/" "${remote}:${REMOTE_ROOT}/"

echo "==> build storage on ${name} (${version})"
"${ssh_args[@]}" "${remote}" \
  "cd ${remote_root_q} && if ! command -v go >/dev/null 2>&1; then for go_bin in \"\$HOME\"/.local/go*/bin; do if [ -x \"\$go_bin/go\" ]; then export PATH=\"\$go_bin:\$PATH\"; break; fi; done; fi && command -v go >/dev/null 2>&1 || { echo 'Go is not installed on storage build host' >&2; exit 1; } && GOFLAGS=-buildvcs=false VERSION=${version_q} GIT_COMMIT=${git_commit_q} CGO_ENABLED=1 TARGET_GOOS=linux TARGET_GOARCH=amd64 bash ./scripts/build.sh storage"

mkdir -p "${BIN_DIR}"
echo "==> download Linux Storage binaries from ${name}"
rsync -az \
  -e "${rsync_ssh}" \
  "${remote}:${REMOTE_ROOT}/bin/moox-storage-primary" \
  "${remote}:${REMOTE_ROOT}/bin/moox-storage-node" \
  "${remote}:${REMOTE_ROOT}/bin/moox-storage-view" \
  "${remote}:${REMOTE_ROOT}/bin/moox-storage-cli" \
  "${BIN_DIR}/"

for binary in moox-storage-primary moox-storage-node moox-storage-view moox-storage-cli; do
  [[ -s "${BIN_DIR}/${binary}" ]] || die "remote build did not return ${binary}"
done

echo "==> Linux Storage binaries built on ${name} and downloaded to ${BIN_DIR}"
