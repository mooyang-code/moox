#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
COMMAND=${1:-}; shift || true
TARGET= DEPLOY_DIR=
while [[ $# -gt 0 ]]; do case "$1" in --target) TARGET=${2:?}; shift 2;; --deploy-dir) DEPLOY_DIR=${2:?}; shift 2;; *) echo "unknown option: $1" >&2; exit 2;; esac; done
[[ "${COMMAND}" =~ ^(check|ensure|status)$ ]] || { echo 'command required: check|ensure|status' >&2; exit 2; }
[[ -n "${TARGET}" && -n "${DEPLOY_DIR}" ]] || { echo '--target and --deploy-dir are required' >&2; exit 2; }
HELPER="${ROOT}/scripts/lib/caddy-managed.sh"
if [[ "${TARGET}" == localhost || "${TARGET}" == 127.0.0.1 || "${TARGET}" == ::1 ]]; then
  exec "${HELPER}" "${COMMAND}" --deploy-dir "${DEPLOY_DIR}"
fi
remote_helper="${DEPLOY_DIR%/}/lib/caddy-managed.sh"
remote_checksums="${DEPLOY_DIR%/}/lib/caddy-v2.11.4-checksums.txt"
ssh_opts=(-o BatchMode=yes -o ConnectTimeout=10)
if [[ "${COMMAND}" == ensure ]]; then
  ssh "${ssh_opts[@]}" "${TARGET}" "mkdir -p $(printf %q "${DEPLOY_DIR%/}/lib")"
  scp "${ROOT}/scripts/lib/caddy-managed.sh" "${ROOT}/scripts/deps/caddy-v2.11.4-checksums.txt" "${TARGET}:$(printf %q "${DEPLOY_DIR%/}/lib/")"
fi
printf -v command '%q %q --deploy-dir %q' "${remote_helper}" "${COMMAND}" "${DEPLOY_DIR}"
exec ssh "${ssh_opts[@]}" "${TARGET}" "MOOX_CADDY_CHECKSUMS=$(printf %q "${remote_checksums}") ${command}"
