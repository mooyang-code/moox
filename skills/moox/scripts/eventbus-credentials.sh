#!/usr/bin/env bash
set -euo pipefail

# Credentials are owned by Admin. This entrypoint intentionally delegates all
# generation/export/rotation to the Admin CLI so release archives never carry
# secrets or private keys.
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
ADMIN_CLI="${MOOX_ADMIN_CLI:-${ROOT}/bin/moox-admin-cli}"
[[ -x "${ADMIN_CLI}" ]] || { echo "moox-admin-cli is required; build the admin module first" >&2; exit 1; }
[[ $# -gt 0 ]] || { echo "usage: eventbus-credentials.sh ensure|export|rotate ..." >&2; exit 2; }
exec "${ADMIN_CLI}" eventbus-credentials "$@"
