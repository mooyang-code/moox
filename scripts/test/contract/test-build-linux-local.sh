#!/usr/bin/env bash
# build-linux-local.sh 的契约测试：用替身 go / docker / build.sh 检查目标分派（CGO 进 Docker、其余本机交叉编译）、
# 镜像按需构建与缓存、挂载与环境、缺 Docker 或校验和时的报错。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
TMP="$(cd "$(mktemp -d "${TMPDIR:-/tmp}/moox-build-linux-local.XXXXXX")" && pwd)"
trap 'rm -rf "${TMP}"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }
sha() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi; }

# 真实仓库里：固定版本的 Go 必须在校验和表里有 amd64 和 arm64 两条，否则编译镜像构建不了。
go_version="$(tr -d '[:space:]' <"${ROOT}/.go-version")"
for arch in amd64 arm64; do
  awk -v f="go${go_version}.linux-${arch}.tar.gz" '$1 == f && $2 ~ /^[0-9a-f]{64}$/ {found=1} END {exit !found}' "${ROOT}/scripts/build/docker/go-sha256.txt" \
    || fail "scripts/build/docker/go-sha256.txt 缺少 go${go_version}.linux-${arch}.tar.gz 的校验和（升级 .go-version 时要同步）"
done

# --- 假仓库：真实的构建脚本与 docker 目录，加假的 build.sh。
FAKE="${TMP}/repo"
mkdir -p "${FAKE}/scripts/build/docker" "${TMP}/bin" "${TMP}/home"
cp "${ROOT}/scripts/build/build-linux-local.sh" "${FAKE}/scripts/build/"
cp "${ROOT}/scripts/build/docker/"* "${FAKE}/scripts/build/docker/"
printf '%s\n' "${go_version}" >"${FAKE}/.go-version"
cat >"${FAKE}/scripts/build/build.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s goos=%s goarch=%s cgo=%s version=%s commit=%s\n' "$1" "${TARGET_GOOS}" "${TARGET_GOARCH}" "${CGO_ENABLED}" "${VERSION}" "${GIT_COMMIT}" >>"${HOST_BUILD_LOG}"
printf 'host-binary-%s\n' "$1" >"${BIN_DIR}/moox-$1"
EOF
chmod +x "${FAKE}/scripts/build/build.sh"
mkdir -p "${FAKE}/.git" "${FAKE}/web/node_modules/pkg" "${FAKE}/bin" "${FAKE}/web/dist" "${FAKE}/modules/storage"
echo secret >"${FAKE}/.git/config"; echo x >"${FAKE}/web/node_modules/pkg/index.js"; echo old >"${FAKE}/bin/moox-old"; echo x >"${FAKE}/web/dist/index.html"
echo keep >"${FAKE}/modules/storage/main.go"

cat >"${TMP}/bin/go" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
# 假 docker：info / image inspect / build / run；run 时按 -v <目录>:/out 在输出目录里写出二进制，并记录整行参数。
cat >"${TMP}/bin/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'docker %s\n' "$*" >>"${DOCKER_LOG}"
case "$1" in
  info) [[ ! -e "${DOCKER_FLAGS}/down" ]] ;;
  image) [[ -e "${DOCKER_FLAGS}/image-built" ]] ;;
  build) touch "${DOCKER_FLAGS}/image-built" ;;
  run)
    # 真实协议：stdin 是源码 tar，容器里解压后构建，stdout 是产物 tar。这里把收到的源码 tar 存下来供检查，
    # 再按 container-build.sh 之后的目标名造出 moox-<目标> 并以 tar 输出。
    cat >"${DOCKER_FLAGS}/source.tar"
    targets=() after=0
    for arg in "$@"; do
      if [[ ${after} -eq 1 ]]; then targets+=("${arg}"); continue; fi
      [[ "${arg}" == container-build ]] && after=1
    done
    work="$(mktemp -d "${TMPDIR:-/tmp}/fake-docker-out.XXXXXX")"
    for t in "${targets[@]}"; do printf 'container-binary-%s\n' "${t}" >"${work}/moox-${t}"; done
    tar -C "${work}" -cf - .
    rm -rf "${work}"
    ;;
esac
EOF
chmod +x "${TMP}/bin/go" "${TMP}/bin/docker"
export PATH="${TMP}/bin:${PATH}" HOME="${TMP}/home"
export HOST_BUILD_LOG="${TMP}/host-build.log" DOCKER_LOG="${TMP}/docker.log" DOCKER_FLAGS="${TMP}/docker-flags"
mkdir -p "${DOCKER_FLAGS}"
: >"${HOST_BUILD_LOG}"; : >"${DOCKER_LOG}"
SCRIPT="${FAKE}/scripts/build/build-linux-local.sh"
OUT="${TMP}/out"
export VERSION=abc12345 GIT_COMMIT=abc12345

# ---------------------------------------------------------------- 不含 CGO：本机交叉编译，不碰 Docker
bash "${SCRIPT}" --out "${OUT}" admin monitor factor-mgr >"${TMP}/stdout.txt" 2>"${TMP}/stderr.txt" || { cat "${TMP}/stderr.txt" >&2; fail "纯 Go 目标应成功"; }
grep -q '^admin goos=linux goarch=amd64 cgo=0 version=abc12345 commit=abc12345$' "${HOST_BUILD_LOG}" || fail "admin 应以 linux/amd64、CGO=0 本机编译：$(cat "${HOST_BUILD_LOG}")"
grep -q '^monitor goos=linux goarch=amd64 cgo=0 ' "${HOST_BUILD_LOG}" || fail "monitor 应本机编译"
grep -q '^factor-mgr goos=linux goarch=amd64 cgo=0 ' "${HOST_BUILD_LOG}" || fail "因子管理是纯 Go 服务，应本机编译（CGO=0），不进 Docker"
[[ ! -s "${DOCKER_LOG}" ]] || fail "纯 Go 目标不该用 Docker：$(cat "${DOCKER_LOG}")"
grep -q "^BUILT:moox-admin:$(sha "${OUT}/moox-admin")\$" "${TMP}/stdout.txt" || fail "stdout 要有 admin 的 BUILT 行"
grep -q "^BUILT:moox-monitor:$(sha "${OUT}/moox-monitor")\$" "${TMP}/stdout.txt" || fail "stdout 要有 monitor 的 BUILT 行"
[[ -z "$(find "${OUT}" -name '.build-marker*' -print -quit)" ]] || fail "不能留下构建标记文件"

# ---------------------------------------------------------------- 含 CGO：Docker 里编，镜像按需构建
: >"${HOST_BUILD_LOG}"
bash "${SCRIPT}" --out "${OUT}" storage-primary >"${TMP}/stdout.txt" 2>"${TMP}/stderr.txt" || { cat "${TMP}/stderr.txt" >&2; fail "CGO 目标应成功"; }
[[ ! -s "${HOST_BUILD_LOG}" ]] || fail "CGO 目标不该在本机编译：$(cat "${HOST_BUILD_LOG}")"
grep -q '^docker info' "${DOCKER_LOG}" || fail "要先确认 Docker 在运行"
build_line="$(grep '^docker build ' "${DOCKER_LOG}")" || fail "镜像不存在时要构建"
[[ "${build_line}" == *"moox-linux-cgo-builder:go${go_version}-"* ]] || fail "镜像标签要带 Go 版本：${build_line}"
[[ "${build_line}" == *"GO_VERSION=${go_version}"* ]] || fail "要把 Go 版本传给镜像构建"
for arch in amd64 arm64; do
  want="$(awk -v f="go${go_version}.linux-${arch}.tar.gz" '$1 == f {print $2}' "${ROOT}/scripts/build/docker/go-sha256.txt")"
  [[ "${build_line}" == *"GO_SHA256_$(printf '%s' "${arch}" | tr a-z A-Z)=${want}"* ]] || fail "要把 ${arch} 的 Go 校验和传给镜像构建"
done
run_line="$(grep '^docker run ' "${DOCKER_LOG}")" || fail "要用 docker run 编译"
# 源码与产物都走 tar 管道：不依赖主机目录能否被 Docker 挂载（colima 默认只共享 $HOME，远程 Docker 完全没有共享）。
[[ "${run_line}" != *":/src"* && "${run_line}" != *":/out"* ]] || fail "不能依赖主机目录挂载：${run_line}"
[[ "${run_line}" == *" -i "* ]] || fail "要用 -i 从 stdin 接收源码"
[[ "${run_line}" == *"-e VERSION=abc12345"* && "${run_line}" == *"-e GIT_COMMIT=abc12345"* ]] || fail "版本信息要传进容器"
[[ "${run_line}" == *"GOPROXY=https://mirrors.tencent.com/go,direct"* ]] || fail "容器里要有 Go 依赖代理"
[[ "${run_line}" == *"container-build storage-primary"* ]] || fail "目标要作为参数传给容器里的构建命令：${run_line}"
[[ "${run_line}" == *"container-build.sh"* ]] || fail "要在容器里运行 container-build.sh：${run_line}"
# 送进容器的源码 tar：要有构建脚本和源码，不能带 .git、node_modules、旧的 bin、前端产物。
listing="$(tar -tf "${DOCKER_FLAGS}/source.tar")"
grep -q 'scripts/build/docker/container-build.sh' <<<"${listing}" || fail "源码 tar 里缺少容器构建脚本"
grep -q 'modules/storage/main.go' <<<"${listing}" || fail "源码 tar 里缺少源码"
for excluded in '\.git/' 'node_modules' '^\./bin/' 'web/dist'; do
  ! grep -Eq "${excluded}" <<<"${listing}" || fail "源码 tar 不该包含 ${excluded}：$(grep -E "${excluded}" <<<"${listing}" | head -3)"
done
grep -q "^BUILT:moox-storage-primary:$(sha "${OUT}/moox-storage-primary")\$" "${TMP}/stdout.txt" || fail "stdout 要有 storage-primary 的 BUILT 行"

# 镜像已存在时不再构建。
: >"${DOCKER_LOG}"
bash "${SCRIPT}" --out "${OUT}" storage-node >/dev/null 2>&1 || fail "第二次 CGO 构建应成功"
! grep -q '^docker build ' "${DOCKER_LOG}" || fail "镜像已存在时不该重新构建"

# 混合目标：纯 Go 的本机编、CGO 的进 Docker，两类产物都要列出。
: >"${HOST_BUILD_LOG}"; : >"${DOCKER_LOG}"
bash "${SCRIPT}" --out "${OUT}" admin storage >"${TMP}/stdout.txt" 2>/dev/null || fail "混合目标应成功"
grep -q '^admin goos=linux' "${HOST_BUILD_LOG}" || fail "混合目标里的 admin 应本机编"
grep -q 'container-build storage$' "${DOCKER_LOG}" || fail "混合目标里的 storage 应进 Docker：$(cat "${DOCKER_LOG}")"
grep -q '^BUILT:moox-admin:' "${TMP}/stdout.txt" && grep -q '^BUILT:moox-storage:' "${TMP}/stdout.txt" || fail "混合目标的产物都要列出"

# ---------------------------------------------------------------- 护栏
expect_fail() {
  local message="$1" pattern="$2"; shift 2
  if bash "${SCRIPT}" --out "${OUT}" "$@" >"${TMP}/stdout.txt" 2>"${TMP}/stderr.txt"; then fail "${message}：本应失败却成功了"; fi
  grep -q -- "${pattern}" "${TMP}/stderr.txt" || fail "${message}：报错里应有「${pattern}」，实际：$(cat "${TMP}/stderr.txt")"
}
expect_fail "没有目标" "没有指定构建目标"
expect_fail "目标不合法" "不合法" "admin;rm"
expect_fail "未知选项" "未知选项" --bogus admin

touch "${DOCKER_FLAGS}/down"
expect_fail "Docker 没运行" "colima start" storage-primary
rm -f "${DOCKER_FLAGS}/down"
: >"${HOST_BUILD_LOG}"
bash "${SCRIPT}" --out "${OUT}" admin >/dev/null 2>&1 || fail "纯 Go 目标不依赖 Docker，Docker 没运行时也应成功"

MOOX_DOCKER=docker-does-not-exist expect_fail "缺 docker 命令" "需要 Docker" storage-primary

printf '# 空表\n' >"${FAKE}/scripts/build/docker/go-sha256.txt"
rm -f "${DOCKER_FLAGS}/image-built"
expect_fail "缺校验和" "go-sha256.txt" storage-primary

echo 'build-linux-local contract passed'
