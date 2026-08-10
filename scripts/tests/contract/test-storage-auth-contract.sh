#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)"
tmp_root="$(mktemp -d)"
trap 'rm -rf "${tmp_root}"' EXIT
control_root="${tmp_root}/prod"
storage_root="${tmp_root}/storage"
mkdir -p "${control_root}/secrets" "${storage_root}/secrets"
printf 'MOOX_STORAGE_PRIMARY_AUTH_SECRET=primary-a\nMOOX_STORAGE_VIEW_AUTH_SECRET=view-a\n' >"${control_root}/secrets/storage-internal-auth.env"
printf 'MOOX_STORAGE_PRIMARY_AUTH_SECRET=primary-a\nMOOX_STORAGE_VIEW_AUTH_SECRET=view-a\n' >"${storage_root}/secrets/storage-internal-auth.env"
chmod 600 "${control_root}/secrets/storage-internal-auth.env" "${storage_root}/secrets/storage-internal-auth.env"

check="${repo_root}/scripts/moox-storage-auth-check.sh"
rotate="${repo_root}/scripts/moox-storage-auth-rotate.sh"
bash "${check}" --control-root "${control_root}" --storage-root "${storage_root}" >/dev/null

printf 'MOOX_STORAGE_PRIMARY_AUTH_SECRET=primary-b\nMOOX_STORAGE_VIEW_AUTH_SECRET=view-a\n' >"${storage_root}/secrets/storage-internal-auth.env"
if bash "${check}" --control-root "${control_root}" --storage-root "${storage_root}" >/dev/null 2>&1; then
  echo "storage auth contract failed: mismatch was accepted" >&2
  exit 1
fi

MOOX_STORAGE_AUTH_BACKUP_DIR="${tmp_root}/backup/storage-auth" bash "${rotate}" --control-root "${control_root}" --storage-root "${storage_root}" \
  --primary primary-c --view view-c --no-restart >/dev/null
bash "${check}" --control-root "${control_root}" --storage-root "${storage_root}" >/dev/null
grep -Fxq 'MOOX_STORAGE_PRIMARY_AUTH_SECRET=primary-c' "${control_root}/secrets/storage-internal-auth.env"
grep -Fxq 'MOOX_STORAGE_PRIMARY_AUTH_SECRET=primary-c' "${storage_root}/secrets/storage-internal-auth.env"
test -f "${tmp_root}/backup/storage-auth/storage-internal-auth-"*'-control.env'
test -f "${tmp_root}/backup/storage-auth/storage-internal-auth-"*'-storage.env'

echo "storage auth consistency contract passed"
