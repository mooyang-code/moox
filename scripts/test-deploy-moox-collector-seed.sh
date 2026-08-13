#!/usr/bin/env bash
set -euo pipefail
exec bash "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)/tests/contract/test-deploy-moox-collector-seed.sh" "$@"
