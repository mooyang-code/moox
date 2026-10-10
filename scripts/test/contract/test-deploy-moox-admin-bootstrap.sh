#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
DEPLOY_SCRIPT="${ROOT}/scripts/deploy/deploy-moox.sh"

bash -n "${DEPLOY_SCRIPT}"

for forbidden in BOOTSTRAP_ADMIN ADMIN_PASSWORD ADMIN_USERNAME ADMIN_PASSWORD_FILE \
  --admin-username --admin-password-file 'user ensure'; do
  if grep -Fq -- "${forbidden}" "${DEPLOY_SCRIPT}"; then
    printf 'deploy script still contains setup-owned Admin bootstrap token: %s\n' "${forbidden}" >&2
    exit 1
  fi
done

grep -Fq -- '--profile <control|storage>' "${DEPLOY_SCRIPT}"
grep -Fq -- '--package-only' "${DEPLOY_SCRIPT}"
grep -Fq 'WITH_ADMIN=1' "${DEPLOY_SCRIPT}"
grep -Fq 'WITH_STORAGE=0' "${DEPLOY_SCRIPT}"

echo 'Admin deployment/setup boundary contract passed'
