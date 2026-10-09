#!/usr/bin/env bash
# MooX 主机运行脚本的公共函数：按发布目录 runtime/ 下的组件规格启动、停止、探测组件。
# 由 start.sh、stop.sh、restart.sh、status.sh、healthcheck.sh、pause.sh、resume.sh 导入。
#
# 目录约定（详见 moox-cli 的 release 包）：
#   ${ROOT}/releases/<版本>   发布目录（RELEASE），${ROOT}/current 指向当前发布
#   ${ROOT}/data、logs、run、secrets、certs   跨发布保留
#   ${ROOT}/run/paused/<组件>   暂停标记：存在时启动脚本和健康检查都不会拉起该组件

set -euo pipefail

RELEASE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
# shellcheck source=/dev/null
source "${RELEASE}/runtime/host.env"
ROOT="${HOST_ROOT}"
MAINTENANCE_LOCK="${ROOT}.maintenance.lock"
HEALTH_FAILURE_THRESHOLD="${MOOX_HEALTHCHECK_FAILURE_THRESHOLD:-3}"

runtime_log() {
  printf '%s %s\n' "$(date '+%Y-%m-%dT%H:%M:%S%z')" "$*"
}

runtime_components() {
  cat "${RELEASE}/runtime/components"
}

runtime_has_component() {
  grep -qx -- "$1" "${RELEASE}/runtime/components"
}

# load_component 导入组件规格，设置 COMPONENT_* 变量和可选的 component_prestart、component_poststart。
load_component() {
  local name="$1"
  runtime_has_component "${name}" || { echo "${name}: 这台主机上没有部署这个组件" >&2; return 1; }
  unset COMPONENT_BINARY COMPONENT_WORKDIR COMPONENT_ARGS COMPONENT_ENV COMPONENT_SECRET_ENV COMPONENT_DATA_DIRS \
    COMPONENT_HEALTH_KIND COMPONENT_HEALTH_PORT COMPONENT_HEALTH_URL COMPONENT_STARTUP_GRACE COMPONENT_STOP_TIMEOUT
  unset -f component_prestart component_poststart 2>/dev/null || true
  # shellcheck source=/dev/null
  source "${RELEASE}/runtime/${name}.env"
}

component_paused() {
  [[ -e "${ROOT}/run/paused/$1" ]]
}

pid_file() {
  printf '%s/run/%s.pid' "${ROOT}" "$1"
}

# process_exe 输出进程的可执行文件路径（Linux 读 /proc，其他系统用 ps，供本机测试使用）。
process_exe() {
  local pid="$1" exe
  if [[ -d "/proc/${pid}" ]]; then
    exe="$(readlink "/proc/${pid}/exe" 2>/dev/null || true)"
    printf '%s' "${exe% (deleted)}"
    return 0
  fi
  ps -o comm= -p "${pid}" 2>/dev/null || true
}

all_pids() {
  local proc
  if [[ -d /proc/self ]]; then
    for proc in /proc/[0-9]*; do
      printf '%s\n' "${proc##*/}"
    done
    return 0
  fi
  ps -axo pid= | tr -d ' '
}

# process_is_component 判断进程是否运行着组件的二进制：任一发布目录下的 bin/<二进制>（升级后旧进程仍是旧版本）。
process_is_component() {
  local pid="$1" binary="$2" exe
  exe="$(process_exe "${pid}")"
  case "${exe}" in
    "${ROOT}"/releases/*/bin/"${binary}") return 0 ;;
  esac
  return 1
}

# component_pids 列出组件的全部进程：PID 文件中的进程，以及任一发布目录下运行着同一二进制的进程。
component_pids() {
  local name="$1" binary="$2" pid
  local -a found=()
  if [[ -r "$(pid_file "${name}")" ]]; then
    pid="$(tr -d '[:space:]' <"$(pid_file "${name}")")"
    if [[ "${pid}" =~ ^[0-9]+$ ]] && kill -0 "${pid}" 2>/dev/null && process_is_component "${pid}" "${binary}"; then
      found+=("${pid}")
    fi
  fi
  while IFS= read -r pid; do
    [[ -n "${pid}" ]] || continue
    [[ " ${found[*]-} " == *" ${pid} "* ]] && continue
    process_is_component "${pid}" "${binary}" && found+=("${pid}")
  done < <(all_pids)
  printf '%s\n' "${found[@]+"${found[@]}"}"
}

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

process_age() {
  local pid="$1" age
  age="$(ps -o etimes= -p "${pid}" 2>/dev/null | tr -d ' ' || true)"
  if [[ "${age}" =~ ^[0-9]+$ ]]; then
    printf '%s' "${age}"
  fi
}

# with_maintenance_lock 在维护锁下执行命令：wait 为 0 时锁被占用就跳过（返回 75），否则最多等待 wait 秒。
# 调用方已持有维护锁时（MOOX_MAINTENANCE_LOCK_HELD=1）直接执行。
with_maintenance_lock() {
  local wait="$1"
  shift
  if [[ "${MOOX_MAINTENANCE_LOCK_HELD:-0}" == 1 ]]; then
    "$@"
    return
  fi
  if command -v flock >/dev/null 2>&1; then
    local status=0
    (
      if [[ "${wait}" == 0 ]]; then
        flock -n 9 || exit 75
      else
        flock -w "${wait}" 9 || exit 75
      fi
      MOOX_MAINTENANCE_LOCK_HELD=1 "$@"
    ) 9>"${MAINTENANCE_LOCK}" || status=$?
    return "${status}"
  fi
  # 没有 flock 的系统（本机测试）用目录锁代替。
  local dir="${MAINTENANCE_LOCK}.d" waited=0
  until mkdir "${dir}" 2>/dev/null; do
    if (( waited >= wait )); then
      return 75
    fi
    sleep 1
    waited=$((waited + 1))
  done
  local status=0
  MOOX_MAINTENANCE_LOCK_HELD=1 "$@" || status=$?
  rmdir "${dir}" 2>/dev/null || true
  return "${status}"
}

component_running() {
  local name="$1"
  load_component "${name}"
  [[ -n "$(component_pids "${name}" "${COMPONENT_BINARY}")" ]]
}

# secret_env_values 读取 secrets 下的 KEY=VALUE 文件，输出可以交给 env 的参数；只接受简单赋值，不执行文件内容。
secret_env_values() {
  local file="${ROOT}/secrets/$1" line key value
  [[ -r "${file}" ]] || { echo "缺少密钥文件 ${file}" >&2; return 1; }
  while IFS= read -r line || [[ -n "${line}" ]]; do
    [[ -z "${line}" || "${line}" == \#* ]] && continue
    key="${line%%=*}"
    value="${line#*=}"
    [[ "${key}" =~ ^[A-Z_][A-Z0-9_]*$ ]] || { echo "${file} 中有无效的行" >&2; return 1; }
    if [[ "${value}" == \'*\' && ${#value} -ge 2 ]]; then
      value="${value:1:${#value}-2}"
    elif [[ "${value}" == \"*\" && ${#value} -ge 2 ]]; then
      value="${value:1:${#value}-2}"
    fi
    printf '%s=%s\n' "${key}" "${value}"
  done <"${file}"
}

# health_header 生成 health HMAC 请求头的值。
health_header() {
  local path="$1" version access secret timestamp nonce body_hash canonical signature
  version="$(secret_env_values health-auth.env | sed -n 's/^MOOX_HEALTH_AUTH_VERSION=//p')"
  access="$(secret_env_values health-auth.env | sed -n 's/^MOOX_HEALTH_AUTH_ACCESS_KEY=//p')"
  secret="$(secret_env_values health-auth.env | sed -n 's/^MOOX_HEALTH_AUTH_SECRET_KEY=//p')"
  [[ -n "${version}" && -n "${access}" && -n "${secret}" ]] || return 1
  timestamp="$(date +%s)"
  nonce="$(openssl rand -hex 32)"
  body_hash="$(printf '' | openssl dgst -sha256 | awk '{print $NF}')"
  canonical="$(printf 'moox-request-v1\nGET\n%s\n%s\n%s\n%s' "${path}" "${body_hash}" "${timestamp}" "${nonce}")"
  signature="$(printf '%s' "${canonical}" | openssl dgst -sha256 -hmac "${secret}" | awk '{print $NF}')"
  printf '%s/%s/%s/%s/%s' "${version}" "${access}" "${timestamp}" "${nonce}" "${signature}"
}

# probe 探测已导入规格的组件：mode 为 ready（/readyz）或 live（/healthz）。
probe() {
  local mode="$1" path=/readyz header
  case "${COMPONENT_HEALTH_KIND}" in
    readyz)
      [[ "${mode}" == live ]] && path=/healthz
      header="$(health_header "${path}")" || return 1
      curl --fail --silent --max-time 3 -H "X-Moox-Health-Auth: ${header}" \
        "http://127.0.0.1:${COMPONENT_HEALTH_PORT}${path}" >/dev/null
      ;;
    https)
      local hostport="${COMPONENT_HEALTH_URL#https://}"
      hostport="${hostport%%/*}"
      local code
      code="$(curl --silent --insecure --max-time 5 --output /dev/null --write-out '%{http_code}' \
        --resolve "${hostport}:127.0.0.1" "${COMPONENT_HEALTH_URL}" || true)"
      [[ "${code}" =~ ^[23][0-9][0-9]$ ]]
      ;;
    *)
      return 0
      ;;
  esac
}

# listener_open 判断健康端口是否已经在监听：返回 401、503 也说明进程已经起来了。
listener_open() {
  case "${COMPONENT_HEALTH_KIND}" in
    readyz) curl --silent --output /dev/null --connect-timeout 1 --max-time 2 "http://127.0.0.1:${COMPONENT_HEALTH_PORT}/healthz" ;;
    https) probe ready ;;
    *) return 0 ;;
  esac
}

stop_component() {
  local name="$1" pid waited=0
  load_component "${name}"
  local -a pids=()
  mapfile -t pids < <(component_pids "${name}" "${COMPONENT_BINARY}")
  if [[ ${#pids[@]} -eq 0 || -z "${pids[0]}" ]]; then
    rm -f "$(pid_file "${name}")"
    echo "${name}: 未运行"
    return 0
  fi
  echo "${name}: 停止 pid=${pids[*]}"
  kill "${pids[@]}" 2>/dev/null || true
  while (( waited < COMPONENT_STOP_TIMEOUT )); do
    local alive=0
    for pid in "${pids[@]}"; do
      kill -0 "${pid}" 2>/dev/null && alive=1
    done
    (( alive == 0 )) && break
    sleep 1
    waited=$((waited + 1))
  done
  for pid in "${pids[@]}"; do
    if kill -0 "${pid}" 2>/dev/null; then
      echo "${name}: ${COMPONENT_STOP_TIMEOUT} 秒内没有退出，强制结束 pid=${pid}" >&2
      kill -9 "${pid}" 2>/dev/null || true
    fi
  done
  rm -f "$(pid_file "${name}")"
}

# start_component 启动一个组件并等待就绪；force 为 1 时忽略暂停标记。
start_component() {
  local name="$1" force="${2:-0}"
  load_component "${name}"
  if component_paused "${name}" && [[ "${force}" != 1 ]]; then
    echo "${name}: 已暂停，不启动"
    return 0
  fi
  if [[ -n "$(component_pids "${name}" "${COMPONENT_BINARY}")" ]]; then
    echo "${name}: 已在运行"
    return 0
  fi
  local log_dir="${ROOT}/logs/${name}" dir
  mkdir -p "${log_dir}" "${ROOT}/run"
  for dir in "${COMPONENT_DATA_DIRS[@]+"${COMPONENT_DATA_DIRS[@]}"}"; do
    mkdir -p "${ROOT}/${dir}"
  done
  if declare -F component_prestart >/dev/null; then
    echo "${name}: 启动前准备"
    if ! (cd "${RELEASE}" && component_prestart) >>"${log_dir}/stdout.log" 2>&1; then
      echo "${name}: 启动前准备失败，见 ${log_dir}/stdout.log" >&2
      tail -20 "${log_dir}/stdout.log" >&2 || true
      return 1
    fi
  fi
  local binary="${RELEASE}/bin/${COMPONENT_BINARY}" boot_id
  [[ -x "${binary}" ]] || { echo "${name}: 缺少可执行文件 ${binary}" >&2; return 1; }
  boot_id="$(cat /proc/sys/kernel/random/uuid 2>/dev/null || uuidgen 2>/dev/null || printf 'boot-%s-%s' "$(date +%s)" "$$")"
  boot_id="$(printf '%s' "${boot_id}" | tr '[:upper:]' '[:lower:]')"
  local -a env=(
    "HOME=${HOME}" "USER=${USER:-$(id -un)}" "LOGNAME=${LOGNAME:-$(id -un)}"
    "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin" "LANG=${LANG:-C.UTF-8}"
    "MOOX_BOOT_ID=${boot_id}"
    "MOOX_BINARY_SHA256=sha256:$(sha256_of "${binary}")"
  )
  env+=("${COMPONENT_ENV[@]+"${COMPONENT_ENV[@]}"}")
  local file
  for file in "${COMPONENT_SECRET_ENV[@]+"${COMPONENT_SECRET_ENV[@]}"}"; do
    local -a values=()
    mapfile -t values < <(secret_env_values "${file}") || return 1
    env+=("${values[@]+"${values[@]}"}")
  done
  echo "${name}: 启动"
  local detach=()
  command -v setsid >/dev/null 2>&1 && detach=(setsid)
  (
    cd "${RELEASE}/${COMPONENT_WORKDIR}"
    nohup "${detach[@]+"${detach[@]}"}" env -i "${env[@]}" "${binary}" "${COMPONENT_ARGS[@]+"${COMPONENT_ARGS[@]}"}" \
      </dev/null >>"${log_dir}/stdout.log" 2>&1 &
    echo $! >"$(pid_file "${name}")"
  )
  local pid waited=0 timeout="${COMPONENT_STARTUP_GRACE}"
  pid="$(cat "$(pid_file "${name}")")"
  while true; do
    if ! kill -0 "${pid}" 2>/dev/null; then
      echo "${name}: 进程退出，见 ${log_dir}/stdout.log" >&2
      tail -40 "${log_dir}/stdout.log" >&2 || true
      rm -f "$(pid_file "${name}")"
      return 1
    fi
    if probe ready; then
      break
    fi
    if (( waited >= timeout )); then
      echo "${name}: ${timeout} 秒内没有就绪，见 ${log_dir}/stdout.log" >&2
      tail -40 "${log_dir}/stdout.log" >&2 || true
      return 1
    fi
    sleep 1
    waited=$((waited + 1))
  done
  if declare -F component_poststart >/dev/null; then
    if ! (cd "${RELEASE}" && component_poststart) >>"${log_dir}/stdout.log" 2>&1; then
      echo "${name}: 就绪后的处理失败，见 ${log_dir}/stdout.log" >&2
      return 1
    fi
  fi
  echo "${name}: 就绪 pid=${pid}"
}

status_component() {
  local name="$1" pids
  load_component "${name}"
  pids="$(component_pids "${name}" "${COMPONENT_BINARY}" | tr '\n' ' ')"
  local paused=""
  component_paused "${name}" && paused="（已暂停）"
  if [[ -z "${pids// /}" ]]; then
    echo "${name}: 未运行${paused}"
    return 1
  fi
  if probe ready; then
    echo "${name}: 运行中 pid=${pids% } 就绪${paused}"
    return 0
  fi
  echo "${name}: 运行中 pid=${pids% } 未就绪${paused}"
  return 1
}

# ensure_component 由健康检查调用：暂停的组件不管；进程不在就启动；存活探测连续失败达到阈值才重启。
# 就绪探测失败但存活探测正常时不重启（例如存储视图在消化积压）。
ensure_component() {
  local name="$1" failures_file failures pid age
  load_component "${name}"
  component_paused "${name}" && return 0
  failures_file="${ROOT}/run/${name}.health-failures"
  pid="$(component_pids "${name}" "${COMPONENT_BINARY}" | head -1)"
  if [[ -n "${pid}" ]]; then
    if probe ready || probe live; then
      rm -f "${failures_file}"
      return 0
    fi
    age="$(process_age "${pid}")"
    if [[ -n "${age}" ]] && (( age < COMPONENT_STARTUP_GRACE )) && listener_open; then
      return 0
    fi
    failures="$(cat "${failures_file}" 2>/dev/null || echo 0)"
    [[ "${failures}" =~ ^[0-9]+$ ]] || failures=0
    failures=$((failures + 1))
    if (( failures < HEALTH_FAILURE_THRESHOLD )); then
      echo "${failures}" >"${failures_file}"
      runtime_log "${name}: 健康检查失败 pid=${pid}（${failures}/${HEALTH_FAILURE_THRESHOLD}），暂不重启"
      return 0
    fi
    rm -f "${failures_file}"
    runtime_log "${name}: 健康检查连续失败 ${failures} 次，重启"
    stop_component "${name}"
  else
    runtime_log "${name}: 未运行，启动"
  fi
  start_component "${name}"
}
