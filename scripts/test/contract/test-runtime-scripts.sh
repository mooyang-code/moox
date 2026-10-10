#!/usr/bin/env bash
# 主机运行脚本（deploy/runtime）的契约测试：在临时目录里用两个假组件安装发布，验证
# 启动、暂停标记、健康检查、重新安装、维护锁（含 --maintenance-lock-held）、只部署部分组件、启动失败切回和回滚。
# 需要 Linux 上的 bash 4 以上和 flock（与 MooX 主机一致）。
set -euo pipefail

(( BASH_VERSINFO[0] >= 4 )) || { echo "test-runtime-scripts: 需要 bash 4 以上" >&2; exit 1; }
command -v flock >/dev/null 2>&1 || { echo "test-runtime-scripts: 需要 flock（在 Linux 上运行）" >&2; exit 1; }

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
RUNTIME="${REPO}/deploy/runtime"
WORK="$(mktemp -d)"
WORK="$(cd "${WORK}" && pwd -P)"
HOST_ROOT="${WORK}/host"
export MOOX_SKIP_CRON=1
# 组件用 env -i 启动，不能继承安装器的环境变量。
export MOOX_TEST_LEAK=leaked

cleanup() {
  if [[ -x "${HOST_ROOT}/stop.sh" ]]; then
    "${HOST_ROOT}/stop.sh" >/dev/null 2>&1 || true
  fi
  pkill -f "${HOST_ROOT}/releases/" 2>/dev/null || true
  rm -rf "${WORK}"
}
trap cleanup EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

pass() {
  echo "ok - $*"
}

# 假组件：收到 SIGTERM 才退出的小程序。用 go 构建，没有 go 的主机用 MOOX_TEST_FAKE_BINARY 指定（例如交叉编译好的）。
FAKE="${MOOX_TEST_FAKE_BINARY:-}"
if [[ -z "${FAKE}" ]]; then
  command -v go >/dev/null 2>&1 || fail "需要 go 构建假组件，或用 MOOX_TEST_FAKE_BINARY 指定"
  mkdir -p "${WORK}/fake"
  cat >"${WORK}/fake/main.go" <<'EOF'
package main

import (
	"os"
	"os/signal"
	"syscall"
)

func main() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, os.Interrupt)
	<-signals
}
EOF
  (cd "${WORK}/fake" && GOWORK=off GO111MODULE=off CGO_ENABLED=0 go build -o fake-component main.go)
  FAKE="${WORK}/fake/fake-component"
fi
[[ -x "${FAKE}" ]] || fail "假组件 ${FAKE} 不可执行"

# component_spec 写一个组件的运行规格。
component_spec() {
  local name="$1"
  cat <<EOF
COMPONENT_BINARY='moox-test-${name}'
COMPONENT_WORKDIR='.'
COMPONENT_ARGS=()
COMPONENT_ENV=('MOOX_SERVICE_NAME=${name}')
COMPONENT_SECRET_ENV=()
COMPONENT_DATA_DIRS=('data/${name}')
COMPONENT_HEALTH_KIND='none'
COMPONENT_HEALTH_PORT=''
COMPONENT_HEALTH_URL=''
COMPONENT_STARTUP_GRACE='3'
COMPONENT_STOP_TIMEOUT='5'
EOF
}

# make_release <版本> [--bad-beta]：生成发布包 ${WORK}/<版本>.tar.gz，--bad-beta 时发布中没有 beta 的二进制，启动失败。
make_release() {
  local id="$1" bad="${2:-}" stage="${WORK}/stage-${id}" script
  mkdir -p "${stage}/runtime" "${stage}/bin" "${stage}/lib"
  for script in install.sh start.sh stop.sh restart.sh status.sh healthcheck.sh pause.sh resume.sh; do
    cp "${RUNTIME}/${script}" "${stage}/${script}"
  done
  cp "${RUNTIME}/lib/runtime.sh" "${RUNTIME}/lib/log-rotate.sh" "${stage}/lib/"
  printf "HOST_ID='test'\nHOST_ROOT='%s'\nHOST_ADDRESS='127.0.0.1'\nRELEASE_VERSION='%s'\nLOCAL_LOG_MAX_SIZE_MB=50\nLOCAL_LOG_BACKUP_COUNT=5\n" \
    "${HOST_ROOT}" "${id}" >"${stage}/runtime/host.env"
  printf 'alpha\nbeta\n' >"${stage}/runtime/components"
  component_spec alpha >"${stage}/runtime/alpha.env"
  component_spec beta >"${stage}/runtime/beta.env"
  cp "${FAKE}" "${stage}/bin/moox-test-alpha"
  if [[ "${bad}" == --bad-beta ]]; then
    printf 'moox-test-alpha\n' >"${stage}/runtime/binaries"
  else
    printf 'moox-test-alpha\nmoox-test-beta\n' >"${stage}/runtime/binaries"
    cp "${FAKE}" "${stage}/bin/moox-test-beta"
  fi
  tar -czf "${WORK}/${id}.tar.gz" -C "${stage}" .
}

install_release() {
  local id="$1"
  shift
  bash "${WORK}/stage-${id}/install.sh" --root "${HOST_ROOT}" --archive "${WORK}/${id}.tar.gz" --release "${id}" "$@"
}

# release_of <组件>：组件进程所在的发布。
release_of() {
  local pid exe
  pid="$(tr -d '[:space:]' <"${HOST_ROOT}/run/$1.pid")"
  exe="$(readlink "/proc/${pid}/exe")"
  exe="${exe#"${HOST_ROOT}/releases/"}"
  printf '%s' "${exe%%/*}"
}

# running <组件>：PID 文件中的进程存在且运行着某个发布中的组件二进制（status.sh 把暂停的组件算作正常，不能用它判断）。
running() {
  local pidfile="${HOST_ROOT}/run/$1.pid" pid exe
  [[ -r "${pidfile}" ]] || return 1
  pid="$(tr -d '[:space:]' <"${pidfile}")"
  exe="$(readlink "/proc/${pid}/exe" 2>/dev/null)" || return 1
  [[ "${exe}" == "${HOST_ROOT}/releases/"*"/bin/moox-test-$1" ]]
}

current_release() {
  local target
  target="$(readlink "${HOST_ROOT}/current")"
  printf '%s' "${target##*/}"
}

for id in r1 r2 r3 r4 r5 r6; do
  make_release "${id}"
done
make_release r7 --bad-beta

# 1. 首次安装：全部组件启动，根目录有转到当前发布的脚本。
install_release r1 >/dev/null
[[ "$(current_release)" == r1 ]] || fail "首次安装后 current 应指向 r1"
running alpha && running beta || fail "首次安装后组件应在运行"
[[ -x "${HOST_ROOT}/healthcheck.sh" && -x "${HOST_ROOT}/pause.sh" ]] || fail "缺少根目录的运行脚本"
pass "首次安装启动全部组件"

# 1a. 组件的环境：只有规格中的变量和启动时注入的二进制摘要、启动 ID，不继承调用方的环境。
alpha_pid="$(tr -d '[:space:]' <"${HOST_ROOT}/run/alpha.pid")"
environ="$(tr '\0' '\n' <"/proc/${alpha_pid}/environ")"
digest="$(sha256sum "${HOST_ROOT}/releases/r1/bin/moox-test-alpha" | awk '{print $1}')"
grep -qx "MOOX_BINARY_SHA256=sha256:${digest}" <<<"${environ}" || fail "缺少 MOOX_BINARY_SHA256"
grep -q '^MOOX_BOOT_ID=.' <<<"${environ}" || fail "缺少 MOOX_BOOT_ID"
grep -qx 'MOOX_SERVICE_NAME=alpha' <<<"${environ}" || fail "缺少规格中的环境变量"
if grep -q '^MOOX_TEST_LEAK=' <<<"${environ}"; then
  fail "组件继承了调用方的环境变量"
fi
[[ -d "${HOST_ROOT}/data/alpha" ]] || fail "没有创建组件的数据目录"
pass "组件以干净的环境启动并带有二进制摘要"

# 2. 安装器退出后维护锁必须已释放（组件进程不能继承锁的描述符）。
flock -n "${HOST_ROOT}.maintenance.lock" true || fail "安装结束后维护锁仍被占用"
pass "安装结束后维护锁已释放"

# 3. 暂停：写入标记并停止；start.sh 和 healthcheck.sh 都不拉起。
"${HOST_ROOT}/pause.sh" alpha >/dev/null
[[ -e "${HOST_ROOT}/run/paused/alpha" ]] || fail "pause.sh 没有写入暂停标记"
running alpha && fail "暂停后 alpha 仍在运行"
"${HOST_ROOT}/start.sh" >/dev/null
running alpha && fail "start.sh 拉起了暂停的组件"
"${HOST_ROOT}/healthcheck.sh"
running alpha && fail "healthcheck.sh 拉起了暂停的组件"
running beta || fail "beta 应保持运行"
pass "暂停的组件不会被 start.sh 和 healthcheck.sh 拉起"

# 4. 健康检查拉起停止的组件（未暂停）。
kill "$(tr -d '[:space:]' <"${HOST_ROOT}/run/beta.pid")"
sleep 1
running beta && fail "beta 应已停止"
"${HOST_ROOT}/healthcheck.sh"
running beta || fail "healthcheck.sh 没有拉起停止的 beta"
pass "健康检查拉起停止的组件"

# 5. 维护锁被占用时健康检查整体跳过。
kill "$(tr -d '[:space:]' <"${HOST_ROOT}/run/beta.pid")"
sleep 1
flock "${HOST_ROOT}.maintenance.lock" "${HOST_ROOT}/healthcheck.sh"
running beta && fail "维护锁被占用时健康检查不应拉起组件"
"${HOST_ROOT}/healthcheck.sh"
running beta || fail "释放维护锁后健康检查应拉起 beta"
pass "维护锁被占用时健康检查跳过"

# 6. 重新安装：暂停标记仍然有效。
install_release r2 >/dev/null
[[ "$(current_release)" == r2 ]] || fail "重新安装后 current 应指向 r2"
[[ -e "${HOST_ROOT}/run/paused/alpha" ]] || fail "重新安装丢失了暂停标记"
running alpha && fail "重新安装拉起了暂停的组件"
[[ "$(release_of beta)" == r2 ]] || fail "beta 应运行在 r2"
pass "重新安装后暂停标记仍然有效"

# 7. 恢复：删除标记并启动。
"${HOST_ROOT}/resume.sh" alpha >/dev/null
[[ -e "${HOST_ROOT}/run/paused/alpha" ]] && fail "resume.sh 没有删除暂停标记"
running alpha || fail "resume.sh 没有启动 alpha"
[[ "$(release_of alpha)" == r2 ]] || fail "alpha 应运行在 r2"
pass "恢复后组件启动"

# 8. 调用方已持有维护锁时，安装器与 pause/resume 不再加锁，不会死锁。
exec 7>"${HOST_ROOT}.maintenance.lock"
flock 7
timeout 60 bash "${WORK}/stage-r3/install.sh" --root "${HOST_ROOT}" --archive "${WORK}/r3.tar.gz" --release r3 \
  --maintenance-lock-held >/dev/null || fail "持锁时带 --maintenance-lock-held 的安装没有完成"
MOOX_MAINTENANCE_LOCK_HELD=1 timeout 30 "${HOST_ROOT}/pause.sh" alpha >/dev/null || fail "持锁时 pause.sh 没有完成"
MOOX_MAINTENANCE_LOCK_HELD=1 timeout 30 "${HOST_ROOT}/resume.sh" alpha >/dev/null || fail "持锁时 resume.sh 没有完成"
flock -u 7
exec 7>&-
[[ "$(current_release)" == r3 ]] || fail "持锁安装后 current 应指向 r3"
running alpha && running beta || fail "持锁安装后组件应在运行"
pass "--maintenance-lock-held 时安装与暂停不死锁"

# 9. 只部署部分组件：其余组件继续运行在旧发布上，旧发布不会被清理。
alpha_release="$(release_of alpha)"
install_release r4 --components beta >/dev/null
install_release r5 --components beta >/dev/null
install_release r6 --components beta >/dev/null
[[ "$(release_of beta)" == r6 ]] || fail "beta 应运行在 r6"
[[ "$(release_of alpha)" == "${alpha_release}" ]] || fail "只部署 beta 时 alpha 不应重启"
[[ -d "${HOST_ROOT}/releases/${alpha_release}" ]] || fail "仍有进程在用的发布 ${alpha_release} 被清理"
[[ ! -d "${HOST_ROOT}/releases/r1" ]] || fail "没有进程在用的旧发布 r1 应被清理"
pass "只部署部分组件时保留仍在使用的发布"

# 10. 启动失败：切回上一个发布并重新启动。
if install_release r7 >/dev/null 2>&1; then
  fail "beta 无法启动时安装应失败"
fi
[[ "$(current_release)" == r6 ]] || fail "启动失败后应切回 r6，当前是 $(current_release)"
running beta || fail "切回后 beta 应重新启动"
[[ "$(release_of beta)" == r6 ]] || fail "切回后 beta 应运行在 r6"
pass "启动失败时切回上一个发布"

# 11. 回滚：切回上一个发布并重启全部组件。
bash "${HOST_ROOT}/current/install.sh" --root "${HOST_ROOT}" --rollback >/dev/null
[[ "$(current_release)" == r5 ]] || fail "回滚后 current 应指向 r5，当前是 $(current_release)"
running alpha && running beta || fail "回滚后组件应在运行"
[[ "$(release_of alpha)" == r5 && "$(release_of beta)" == r5 ]] || fail "回滚后组件应运行在 r5"
pass "回滚切回上一个发布"

"${HOST_ROOT}/stop.sh" >/dev/null
running alpha && fail "stop.sh 后 alpha 仍在运行"
pass "stop.sh 停止全部组件"
echo "test-runtime-scripts: 全部通过"
