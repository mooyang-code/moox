#!/usr/bin/env bash
set -euo pipefail

# Factor Manager and CLI are the Factor targets requiring Linux CGO.
export MOOX_LINUX_CGO_TARGET="${MOOX_LINUX_CGO_TARGET:-factor-mgr}"
exec "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/build-storage-linux.sh" "$@"
