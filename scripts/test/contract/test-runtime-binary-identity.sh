#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)"
DEPLOY="${ROOT}/scripts/deploy/deploy-moox.sh"

grep -Fq 'MOOX_BINARY_SHA256=sha256:${binary_hash}' "${DEPLOY}"
grep -Fq 'MOOX_VERSION=sha256:${binary_hash:0:12}' "${DEPLOY}"
grep -Fq 'sha256_file "/proc/${pid}/exe"' "${DEPLOY}"
grep -Fq '>> "${log_file}" 2>&1' "${DEPLOY}"
grep -Fq 'factor) url=http://127.0.0.1:11414/readyz; health_path=/readyz' "${DEPLOY}"
grep -Fq 'storage-node) url=http://127.0.0.1:20212/readyz; health_path=/readyz' "${DEPLOY}"
grep -Fq 'factor|storage-node) return 1' "${DEPLOY}"
grep -Fq '[[ "${WITH_HOSTAGENT}" == "1" ]] && "${DEPLOY_DIR}/stop.sh" hostagent || true' "${DEPLOY}"
grep -Fq '[[ "${WITH_HOSTAGENT}" == "0" ]] || "${DEPLOY_DIR}/start.sh" hostagent 8>&-' "${DEPLOY}"
if grep -Fq -- "--exclude='./start.sh'" "${DEPLOY}" || grep -Fq -- "--exclude='./healthcheck.sh'" "${DEPLOY}"; then
  echo 'component overlays must update lifecycle and healthcheck scripts' >&2
  exit 1
fi

echo 'runtime binary identity contract passed'
