#!/usr/bin/env bash
set -euo pipefail
exec "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)/tests/contract/test-trade-dns-resolver-contract.sh" "$@"
