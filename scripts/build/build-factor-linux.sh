#!/usr/bin/env bash
set -euo pipefail

# 在编译主机上构建因子管理服务（CGO）的包装脚本。
export MOOX_LINUX_CGO_TARGET="${MOOX_LINUX_CGO_TARGET:-factor-mgr}"
exec "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/build-storage-linux.sh" "$@"
