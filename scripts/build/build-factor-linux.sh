#!/usr/bin/env bash
set -euo pipefail

# Thin wrapper around the compile-host CGO builder for Factor control and engine.
export MOOX_LINUX_CGO_TARGET="${MOOX_LINUX_CGO_TARGET:-factor}"
exec "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/build-storage-linux.sh" "$@"
