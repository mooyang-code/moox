#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
exec bash "${ROOT}/scripts/tests/contract/test-deploy-moox-collector-dns-resolver.sh" "$@"
