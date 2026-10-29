#!/usr/bin/env bash
# 在本机构建 linux/amd64 二进制：
#   - 不含 CGO 的目标用本机固定版本的 Go 交叉编译；
#   - 含 CGO 的目标（Storage 的 DuckDB）在 Docker 编译镜像里编（镜像见 scripts/build/docker/）。
#
# 用法：build-linux-local.sh [--out 输出目录] <构建目标>...
#   构建目标即 scripts/build/build.sh 的目标，例如 admin monitor collector storage-primary。
#   输出目录默认 bin/（可以是相对路径）；每个构建出的文件在 stdout 打印一行 BUILT:<文件名>:<sha256>，进度信息走 stderr。
#   不支持 all：逐个写明目标。
#
# 环境变量：
#   VERSION / GIT_COMMIT   写进二进制的版本信息，默认取当前提交。
#   MOOX_DOCKER            docker 命令，默认 docker。
#   MOOX_BUILD_GOPROXY     容器里下载 Go 依赖用的代理，默认 https://mirrors.tencent.com/go,direct 。
#   MOOX_BUILD_APT_MIRROR  构建镜像时的 apt 镜像（如 http://mirrors.aliyun.com ，必须是不跳转 https 的 http 镜像，原因见 Dockerfile），默认用镜像自带的源。
#   MOOX_BUILD_GO_DL_BASE  构建镜像时下载 Go 的地址前缀，默认 https://golang.google.cn/dl 。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DOCKER="${MOOX_DOCKER:-docker}"
OUT="${ROOT}/bin"
targets=()

die() { echo "build-linux-local: $*" >&2; exit 1; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --out) OUT="${2:-}"; [[ -n "${OUT}" ]] || die "--out 需要目录"; shift 2 ;;
    -h|--help) sed -n '2,15p' "${BASH_SOURCE[0]}"; exit 0 ;;
    --*) die "未知选项 $1" ;;
    *) targets+=("$1"); shift ;;
  esac
done
[[ ${#targets[@]} -gt 0 ]] || die "没有指定构建目标"
for target in "${targets[@]}"; do
  [[ "${target}" =~ ^[a-z0-9][a-z0-9-]*$ ]] || die "构建目标 ${target} 不合法"
  # all 里包含 Storage：按纯 Go 关闭 CGO 构建会得到启动即报错的 storage-view。逐个写明目标，才能正确选择本机还是 Docker。
  case "${target}" in
    all) die "不支持 all：请逐个指定目标（例如 admin monitor collector storage-primary），含 CGO 的目标会自动进 Docker" ;;
    proto) die "proto 不是二进制目标：用 make proto 生成协议代码" ;;
  esac
done

go_version="$(tr -d '[:space:]' < "${ROOT}/.go-version")"
# 固定版本的 Go 装在 ~/.local/go<版本>/bin 时优先用它，与 CI 的运行器步骤一致。
if [[ -d "${HOME}/.local/go${go_version}/bin" ]]; then
  export PATH="${HOME}/.local/go${go_version}/bin:${PATH}"
fi

# 含 CGO 的目标与 build.sh 的 CGO 默认一致：Storage 的三个进程依赖 DuckDB。
cgo_targets=() plain_targets=()
for target in "${targets[@]}"; do
  case "${target}" in
    storage|storage-cgo|storage-primary|storage-node) cgo_targets+=("${target}") ;;
    *) plain_targets+=("${target}") ;;
  esac
done

version="${VERSION:-$(git -C "${ROOT}" rev-parse --short=8 HEAD 2>/dev/null || echo dev)}"
commit="${GIT_COMMIT:-${version}}"

# 输出目录先转成绝对路径：build.sh 会先进入各模块目录再写文件，相对路径会让产物落到模块目录里。
mkdir -p "${OUT}"
OUT="$(cd "${OUT}" && pwd)"
# 每次构建用独立的暂存目录，构建成功后只把这个目录里的产物拷到输出目录并列出；不比较修改时间
# （容器与宿主机的时钟可能有偏差，输出目录里也可能留着旧文件）。
STAGE="$(mktemp -d "${TMPDIR:-/tmp}/moox-build-stage.XXXXXX")"
trap 'rm -rf "${STAGE}"' EXIT

if [[ ${#plain_targets[@]} -gt 0 ]]; then
  command -v go >/dev/null || die "找不到 go"
  for target in "${plain_targets[@]}"; do
    echo "==> 本机交叉编译 ${target} (linux/amd64, CGO=0)" >&2
    TARGET_GOOS=linux TARGET_GOARCH=amd64 CGO_ENABLED=0 BIN_DIR="${STAGE}" VERSION="${version}" GIT_COMMIT="${commit}" \
      bash "${ROOT}/scripts/build/build.sh" "${target}" >&2
  done
fi

if [[ ${#cgo_targets[@]} -gt 0 ]]; then
  command -v "${DOCKER}" >/dev/null || die "含 CGO 的目标（${cgo_targets[*]}）需要 Docker：先装好并启动（macOS 可用 colima：colima start）"
  "${DOCKER}" info >/dev/null 2>&1 || die "Docker 没有在运行（macOS 上先执行 colima start）"

  sums="${ROOT}/scripts/build/docker/go-sha256.txt"
  sha_amd64="$(awk -v f="go${go_version}.linux-amd64.tar.gz" '$1 == f {print $2}' "${sums}")"
  sha_arm64="$(awk -v f="go${go_version}.linux-arm64.tar.gz" '$1 == f {print $2}' "${sums}")"
  [[ -n "${sha_amd64}" && -n "${sha_arm64}" ]] || die "${sums} 里没有 Go ${go_version} 的校验和：升级 .go-version 时要同步更新（来源 https://golang.google.cn/dl/?mode=json）"

  # 镜像标签包含 Go 版本和构建文件的摘要，Dockerfile 或校验和变了就会自动重建。
  digest="$(cat "${ROOT}/scripts/build/docker/linux-cgo.Dockerfile" "${sums}" | shasum -a 256 | cut -c1-12)"
  image="moox-linux-cgo-builder:go${go_version}-${digest}"
  if ! "${DOCKER}" image inspect "${image}" >/dev/null 2>&1; then
    echo "==> 构建编译镜像 ${image}" >&2
    "${DOCKER}" build -q -t "${image}" -f "${ROOT}/scripts/build/docker/linux-cgo.Dockerfile" \
      --build-arg "GO_VERSION=${go_version}" --build-arg "GO_SHA256_AMD64=${sha_amd64}" --build-arg "GO_SHA256_ARM64=${sha_arm64}" \
      --build-arg "GO_DL_BASE=${MOOX_BUILD_GO_DL_BASE:-https://golang.google.cn/dl}" \
      --build-arg "APT_MIRROR=${MOOX_BUILD_APT_MIRROR:-}" \
      "${ROOT}/scripts/build/docker" >&2
  fi

  echo "==> 在 Docker 里编译 ${cgo_targets[*]} (linux/amd64, CGO=1)" >&2
  # 源码和产物都用 tar 管道进出，不依赖主机目录能否被 Docker 挂载（colima 默认只共享 $HOME，远程 Docker 更是完全没有共享）。
  # 模块缓存与编译缓存放在具名卷里，重复构建不用重新下载和编译。源码里不带 .git 与 node_modules。
  tar -C "${ROOT}" --exclude=./.git --exclude=./bin --exclude=node_modules --exclude=./web/dist --exclude='./.claude' -cf - . \
    | "${DOCKER}" run --rm -i \
        -v moox-go-mod-cache:/root/go/pkg/mod -v moox-go-build-cache:/root/.cache/go-build \
        -e "VERSION=${version}" -e "GIT_COMMIT=${commit}" -e "GO_STRIP=${GO_STRIP:-}" \
        -e "GOPROXY=${MOOX_BUILD_GOPROXY:-https://mirrors.tencent.com/go,direct}" -e GOFLAGS=-buildvcs=false \
        "${image}" bash -c 'mkdir -p /src /out && tar -x -C /src && bash /src/scripts/build/docker/container-build.sh "$@" && tar -C /out -cf - .' container-build "${cgo_targets[@]}" \
    | tar -x -C "${STAGE}"
fi

sha_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi
}
count=0
for file in "${STAGE}"/*; do
  [[ -f "${file}" ]] || continue
  name="$(basename "${file}")"
  [[ "${name}" =~ ^moox-[a-z0-9-]+$ ]] || continue
  cp -f "${file}" "${OUT}/${name}"
  printf 'BUILT:%s:%s\n' "${name}" "$(sha_of "${OUT}/${name}")"
  count=$((count + 1))
done
[[ ${count} -gt 0 ]] || die "构建没有产出二进制：检查构建目标名是否正确、Go 版本是否为 ${go_version}"
