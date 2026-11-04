#!/usr/bin/env bash
# 在本机的 Docker（Node 容器）里构建前端，产物写回 web/dist；本机就是编译机，不需要远程主机。
# 容器里的依赖是 Linux 版，避开 macOS 上 vite 加载 fsevents 卡住、jsdom 与 vue-tsc 不可用等问题；Node 大版本与 CI 一致。
#
# 用法：build-web-docker.sh [--check]
#   默认：pnpm install --frozen-lockfile 后执行 pnpm build:prod（含 vue-tsc 类型检查），把 dist 拷回 web/dist。
#   --check：构建前先跑与 CI web 组相同的检查（vitest、prettier、eslint）。
#
# 环境变量：
#   MOOX_DOCKER             docker 命令，默认 docker。
#   MOOX_BUILD_NODE_IMAGE   Node 镜像，默认 node:24-bookworm（与 CI 的 node-version 一致）。
#   MOOX_BUILD_NPM_REGISTRY 容器里下载前端依赖用的源，默认 https://registry.npmmirror.com 。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DOCKER="${MOOX_DOCKER:-docker}"
IMAGE="${MOOX_BUILD_NODE_IMAGE:-node:24-bookworm}"
REGISTRY="${MOOX_BUILD_NPM_REGISTRY:-https://registry.npmmirror.com}"
check=0

die() { echo "build-web-docker: $*" >&2; exit 1; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --check) check=1; shift ;;
    -h|--help) sed -n '2,12p' "${BASH_SOURCE[0]}"; exit 0 ;;
    *) die "未知参数 $1" ;;
  esac
done

command -v "${DOCKER}" >/dev/null || die "需要 Docker：先装好并启动（macOS 可用 colima：colima start）"
"${DOCKER}" info >/dev/null 2>&1 || die "Docker 没有在运行（macOS 上先执行 colima start）"

STAGE="$(mktemp -d "${TMPDIR:-/tmp}/moox-web-dist.XXXXXX")"
trap 'rm -rf "${STAGE}"' EXIT

echo "==> 在 Docker（${IMAGE}）里构建前端$([[ ${check} -eq 1 ]] && echo '，并先跑测试与代码检查')" >&2
# 源码和产物都用 tar 管道进出容器，不依赖主机目录能否被挂载。web 的测试会读 packages 下的目录与测试向量，所以一并带上；
# 根目录 package.json 的 packageManager 固定了 pnpm 版本，corepack 向上查找时会用到它（缺了会下载最新的大版本）。
# 只带 Git 跟踪或未被忽略的文件：本机的 node_modules、dist 都不进容器。pnpm 的包存放在具名卷里，重复构建不用重新下载。
(cd "${ROOT}" && git ls-files -z -co --exclude-standard -- package.json web packages | COPYFILE_DISABLE=1 tar --null --no-xattrs -T - -cf -) \
  | "${DOCKER}" run --rm -i -e CI=true -e COREPACK_ENABLE_DOWNLOAD_PROMPT=0 -e "CHECK=${check}" -e "REGISTRY=${REGISTRY}" \
      -v moox-pnpm-store:/pnpm-store "${IMAGE}" bash -c '
        set -euo pipefail
        mkdir -p /src && tar -x -C /src 2>/dev/null
        cd /src/web
        {
          corepack enable
          pnpm config set registry "${REGISTRY}"
          pnpm config set store-dir /pnpm-store
          pnpm install --frozen-lockfile
          if [[ "${CHECK}" == 1 ]]; then
            pnpm test
            pnpm run lint:prettier:check
            pnpm run lint:eslint:check
          fi
          pnpm build:prod
        } >&2
        tar -C /src/web/dist -cf - .
      ' \
  | tar -x -C "${STAGE}"

[[ -d "${STAGE}/static/js" ]] || die "构建产物不完整：dist 里没有 static/js"
rm -rf "${ROOT}/web/dist"
mkdir -p "${ROOT}/web/dist"
cp -R "${STAGE}/." "${ROOT}/web/dist/"
echo "==> 前端已构建到 web/dist（$(find "${ROOT}/web/dist" -type f | wc -l | tr -d ' ') 个文件）" >&2
