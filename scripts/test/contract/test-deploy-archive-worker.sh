#!/usr/bin/env bash
# Contract: deploy-archive-worker.sh validates its inputs, installs a prebuilt binary,
# renders the worker configuration and the service definition, and never starts the service with --no-start.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SCRIPT="${ROOT}/scripts/deploy/deploy-archive-worker.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT

fail() { echo "test-deploy-archive-worker: $*" >&2; exit 1; }

mkdir -p "${tmp}/deploy"
tmp="$(cd "${tmp}" && pwd -P)" # 脚本用 pwd -P 解析安装目录
deploy="${tmp}/deploy"
secrets="${deploy}/secrets"
archive_root="${tmp}/archive-root"
mkdir -p "${secrets}"
chmod 0700 "${secrets}"
printf 'k%.0s' {1..40} >"${secrets}/access-archive-worker.key"
printf 's%.0s' {1..40} >"${secrets}/storage-primary-auth.secret"
chmod 0600 "${secrets}"/*
printf '#!/bin/sh\nexit 0\n' >"${tmp}/moox-archive-worker"
chmod 0755 "${tmp}/moox-archive-worker"

health_env="${tmp}/health-auth.env"
printf 'MOOX_HEALTH_AUTH_VERSION=moox-health-v1\nMOOX_HEALTH_AUTH_ACCESS_KEY=health-access-9\nMOOX_HEALTH_AUTH_SECRET_KEY=%s\n' "$(printf 'h%.0s' {1..64})" >"${health_env}"
chmod 0600 "${health_env}"

export MOOX_SYSTEMD_USER_DIR="${tmp}/systemd"
args=(--dir "${deploy}" --root-dir "${archive_root}" --worker-id archive-worker@testhost --access-address access.example.test:11004
  --access-id access@storage --access-key-id key-123 --binary "${tmp}/moox-archive-worker" --health-env "${health_env}" --no-start)

# 输入校验。
if bash "${SCRIPT}" --dir "${deploy}" --root-dir relative/path --access-address a:1 --access-id access@x --access-key-id k --no-start >"${tmp}/bad1.log" 2>&1; then
  fail "a relative --root-dir must be rejected"
fi
if bash "${SCRIPT}" --dir "${deploy}" --root-dir "${archive_root}" --access-address nope --access-id access@x --access-key-id k --no-start >"${tmp}/bad2.log" 2>&1; then
  fail "a bad --access-address must be rejected"
fi
chmod 0644 "${secrets}/access-archive-worker.key"
if bash "${SCRIPT}" "${args[@]}" >"${tmp}/bad3.log" 2>&1; then
  fail "a credential with a loose mode must be rejected"
fi
chmod 0600 "${secrets}/access-archive-worker.key"

bash "${SCRIPT}" "${args[@]}" >"${tmp}/ok.log" 2>&1 || { cat "${tmp}/ok.log" >&2; fail "deploy with --no-start failed"; }

[[ -x "${deploy}/bin/moox-archive-worker" ]] || fail "binary was not installed"
[[ -d "${archive_root}" ]] || fail "archive root was not created"
config="${deploy}/config/app.yaml"
for want in 'access_address: "access.example.test:11004"' 'access_id: "access@storage"' 'caller: archive-worker' 'key_id: "key-123"' \
  "key_file: ${secrets}/access-archive-worker.key" "storage_auth_file: ${secrets}/storage-primary-auth.secret" 'worker_id: archive-worker@testhost' \
  "root_dir: ${archive_root}" 'addr: 127.0.0.1:11418'; do
  grep -qF -- "${want}" "${config}" || fail "rendered config misses: ${want}"
done
unit="${MOOX_SYSTEMD_USER_DIR}/moox-archive-worker.service"
[[ -s "${unit}" ]] || fail "service definition was not rendered"
grep -qF "${deploy}/bin/moox-archive-worker" "${unit}" || fail "service definition does not run the installed binary"
grep -q '__[A-Z_]*__' "${unit}" && fail "unrendered placeholder in the service definition"
[[ -s "${secrets}/health-auth.env" ]] || fail "health auth env was not installed"
grep -qF 'MOOX_HEALTH_AUTH_ACCESS_KEY=health-access-9' "${secrets}/health-auth.env" || fail "health auth env must come from --health-env"
[[ "$(stat -f '%Lp' "${secrets}/health-auth.env" 2>/dev/null || stat -c '%a' "${secrets}/health-auth.env")" == 600 ]] || fail "health auth env must be mode 0600"
# 再次部署不带 --health-env 时保留已安装的健康检查凭据。
before="$(cat "${secrets}/health-auth.env")"
bash "${SCRIPT}" --dir "${deploy}" --root-dir "${archive_root}" --worker-id archive-worker@testhost --access-address access.example.test:11004 \
  --access-id access@storage --access-key-id key-123 --skip-build --no-start >/dev/null 2>&1 || fail "second deploy failed"
[[ "$(cat "${secrets}/health-auth.env")" == "${before}" ]] || fail "health credentials must be kept across deployments"

echo "deploy-archive-worker contract passed"
