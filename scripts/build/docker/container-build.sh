#!/usr/bin/env bash
# 在编译容器里执行：把解压在 /src 的源码按目标构建成 linux/amd64 二进制，输出到 /out。
# 由 scripts/build/build-linux-local.sh 调用，不要在宿主上直接运行。构建日志全部走 stderr，stdout 留给产物的 tar 流。
set -euo pipefail

[[ -d /src && -d /out ]] || { echo "container-build.sh 只能在编译容器里运行（需要 /src 与 /out）" >&2; exit 1; }

# 宿主是 arm64 时镜像里是 arm64 的编译器，产出 amd64 要用交叉编译器；本身是 amd64 时直接用原生编译器。
case "$(uname -m)" in
  x86_64) ;;
  aarch64)
    export CC=x86_64-linux-gnu-gcc CXX=x86_64-linux-gnu-g++
    ;;
  *) echo "不支持的编译容器架构 $(uname -m)" >&2; exit 1 ;;
esac

export TARGET_GOOS=linux TARGET_GOARCH=amd64 BIN_DIR=/out
mkdir -p /tmp/gotmp
export GOTMPDIR=/tmp/gotmp

cd /src
for target in "$@"; do
  bash scripts/build/build.sh "${target}" >&2
done
