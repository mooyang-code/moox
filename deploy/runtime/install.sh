#!/usr/bin/env bash
# MooX 主机安装器：由 moox-cli 上传发布包后在目标主机上执行。
#
#   install.sh --root <部署根目录> --archive <发布包> --release <版本>
#              [--components a,b] [--generate-secrets x,y] [--no-start] [--maintenance-lock-held]
#   install.sh --root <部署根目录> --rollback [--maintenance-lock-held]
#
# 安装：持有维护锁（--maintenance-lock-held 表示调用方已持有，安装器不再加锁）→ 解出发布目录 → 发布包没带的二进制
# 从当前发布复用 → 把 incoming/ 中的密钥和证书装进 secrets/、certs/ → 生成缺少的本机密钥 → 停止要替换的组件 →
# 切换 current → 启动组件（暂停的组件不启动）。启动失败时切回上一个发布并重新启动这些组件。
# 回滚：切回上一个发布并重启全部组件。
set -euo pipefail

ROOT="" ARCHIVE="" RELEASE_ID="" COMPONENTS="" GENERATE="" NO_START=0 LOCK_HELD=0 ROLLBACK=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --root) ROOT="${2:-}"; shift 2 ;;
    --archive) ARCHIVE="${2:-}"; shift 2 ;;
    --release) RELEASE_ID="${2:-}"; shift 2 ;;
    --components) COMPONENTS="${2:-}"; shift 2 ;;
    --generate-secrets) GENERATE="${2:-}"; shift 2 ;;
    --no-start) NO_START=1; shift ;;
    --maintenance-lock-held) LOCK_HELD=1; shift ;;
    --rollback) ROLLBACK=1; shift ;;
    *) echo "install: 未知参数 $1" >&2; exit 2 ;;
  esac
done

fail() {
  echo "install: $*" >&2
  exit 1
}

[[ "${ROOT}" == /* && "${ROOT}" != / && "${ROOT}" != *..* ]] || fail "--root 必须是绝对路径"
ROOT="${ROOT%/}"
if [[ "${ROLLBACK}" == 0 ]]; then
  [[ "${RELEASE_ID}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]] || fail "--release 无效"
  [[ -f "${ARCHIVE}" ]] || fail "发布包 ${ARCHIVE} 不存在"
fi

acquire_lock() {
  [[ "${LOCK_HELD}" == 1 ]] && return 0
  mkdir -p "$(dirname "${ROOT}")"
  if command -v flock >/dev/null 2>&1; then
    exec 9>"${ROOT}.maintenance.lock"
    flock -w 600 9 || fail "10 分钟内没有拿到维护锁 ${ROOT}.maintenance.lock"
    return 0
  fi
  local waited=0
  until mkdir "${ROOT}.maintenance.lock.d" 2>/dev/null; do
    (( waited < 600 )) || fail "10 分钟内没有拿到维护锁"
    sleep 1
    waited=$((waited + 1))
  done
  trap 'rmdir "${ROOT}.maintenance.lock.d" 2>/dev/null || true' EXIT
}

# 运行脚本由安装器调用时已经持有维护锁。
export MOOX_MAINTENANCE_LOCK_HELD=1

current_release() {
  [[ -L "${ROOT}/current" ]] || return 0
  local target
  target="$(readlink "${ROOT}/current")"
  printf '%s' "${target##*/}"
}

switch_current() {
  local id="$1"
  ln -sfn "releases/${id}" "${ROOT}/current.next"
  if ! mv -fT "${ROOT}/current.next" "${ROOT}/current" 2>/dev/null; then
    rm -f "${ROOT}/current"
    mv "${ROOT}/current.next" "${ROOT}/current"
  fi
}

write_wrappers() {
  local name
  for name in start stop restart status healthcheck pause resume; do
    printf '#!/bin/sh\n# 由 moox-cli 安装，转到当前发布的同名脚本。\nexec "$(dirname "$0")/current/%s.sh" "$@"\n' "${name}" >"${ROOT}/${name}.sh.next"
    chmod 0755 "${ROOT}/${name}.sh.next"
    mv -f "${ROOT}/${name}.sh.next" "${ROOT}/${name}.sh"
  done
}

install_cron() {
  [[ "${MOOX_SKIP_CRON:-0}" == 1 ]] && return 0
  if ! command -v crontab >/dev/null 2>&1; then
    echo "install: 警告：没有 crontab，健康检查不会自动运行" >&2
    return 0
  fi
  local marker="# moox-healthcheck:${ROOT}" line current
  line="* * * * * ${ROOT}/healthcheck.sh >/dev/null 2>&1 ${marker}"
  current="$(crontab -l 2>/dev/null || true)"
  {
    printf '%s\n' "${current}" | grep -Fv -- "${marker}" | sed '/^$/d' || true
    printf '%s\n' "${line}"
  } | crontab -
}

generate_secret() {
  local name="$1" file="${ROOT}/secrets/$1"
  [[ -s "${file}" ]] && return 0
  hex() { openssl rand -hex 32; }
  case "${name}" in
    health-auth.env)
      printf 'MOOX_HEALTH_AUTH_VERSION=moox-health-v1\nMOOX_HEALTH_AUTH_ACCESS_KEY=monitor\nMOOX_HEALTH_AUTH_SECRET_KEY=%s\n' "$(hex)" >"${file}.next" ;;
    storage-internal-auth.env)
      printf 'MOOX_STORAGE_PRIMARY_AUTH_SECRET=%s\nMOOX_STORAGE_VIEW_AUTH_SECRET=%s\n' "$(hex)" "$(hex)" >"${file}.next" ;;
    storage-node-auth.env)
      printf 'MOOX_STORAGE_NODE_AUTH_SECRET=%s\n' "$(hex)" >"${file}.next" ;;
    admin-jwt.env)
      printf 'MOOX_ADMIN_JWT_SECRET_KEY=%s\n' "$(hex)" >"${file}.next" ;;
    admin-encryption-key)
      # 加密密钥丢失后已有的密钥表无法解密，有数据库时不重新生成。
      [[ ! -e "${ROOT}/data/admin/admin.db" ]] || fail "缺少 ${file}，但 ${ROOT}/data/admin/admin.db 已存在；请先恢复原来的加密密钥"
      head -c 32 /dev/urandom | base64 | tr -d '\n' >"${file}.next" ;;
    *) fail "不知道怎样生成密钥 ${name}" ;;
  esac
  chmod 0600 "${file}.next"
  mv -f "${file}.next" "${file}"
  echo "生成 ${file}"
}

# install_incoming 把发布包中的密钥和证书装进部署根目录，然后从发布目录中删除。
install_incoming() {
  local staged="$1" source target
  [[ -d "${staged}/incoming" ]] || return 0
  if [[ -d "${staged}/incoming/secrets" ]]; then
    (cd "${staged}/incoming/secrets" && find . -type f) | while IFS= read -r source; do
      target="${ROOT}/secrets/${source#./}"
      mkdir -p "$(dirname "${target}")"
      chmod 0700 "$(dirname "${target}")"
      install -m 0600 "${staged}/incoming/secrets/${source#./}" "${target}.next"
      mv -f "${target}.next" "${target}"
    done
  fi
  if [[ -d "${staged}/incoming/certs" ]]; then
    (cd "${staged}/incoming/certs" && find . -type f) | while IFS= read -r source; do
      target="${ROOT}/certs/${source#./}"
      mkdir -p "$(dirname "${target}")"
      case "${source}" in
        *.key) install -m 0600 "${staged}/incoming/certs/${source#./}" "${target}.next" ;;
        *) install -m 0644 "${staged}/incoming/certs/${source#./}" "${target}.next" ;;
      esac
      mv -f "${target}.next" "${target}"
    done
  fi
  rm -rf "${staged}/incoming"
}

# reuse_binaries 让发布包没带的二进制（只部署部分组件时）复用当前发布中的同名文件。
reuse_binaries() {
  local staged="$1" previous="$2" binary
  [[ -r "${staged}/runtime/binaries" ]] || return 0
  while IFS= read -r binary; do
    [[ -n "${binary}" ]] || continue
    [[ -e "${staged}/bin/${binary}" ]] && continue
    [[ -n "${previous}" && -x "${ROOT}/releases/${previous}/bin/${binary}" ]] ||
      fail "发布包没有 ${binary}，当前发布里也没有；首次部署要包含全部组件"
    mkdir -p "${staged}/bin"
    ln "${ROOT}/releases/${previous}/bin/${binary}" "${staged}/bin/${binary}" 2>/dev/null ||
      cp -p "${ROOT}/releases/${previous}/bin/${binary}" "${staged}/bin/${binary}"
  done <"${staged}/runtime/binaries"
}

# release_in_use 判断是否还有进程在运行某个发布中的二进制：只部署部分组件时，其余组件仍运行在旧发布上。
release_in_use() {
  local id="$1" proc exe
  for proc in /proc/[0-9]*; do
    exe="$(readlink "${proc}/exe" 2>/dev/null)" || continue
    [[ "${exe}" == "${ROOT}/releases/${id}/"* ]] && return 0
  done
  return 1
}

prune_releases() {
  local keep_current="$1" keep_previous="$2" id count=0
  # 保留当前、上一个和再往前的一个发布，以及仍有进程在运行的发布。
  while IFS= read -r id; do
    [[ -n "${id}" ]] || continue
    [[ "${id}" == "${keep_current}" || "${id}" == "${keep_previous}" ]] && continue
    count=$((count + 1))
    (( count > 1 )) || continue
    release_in_use "${id}" || rm -rf "${ROOT}/releases/${id}"
  done < <(ls -1t "${ROOT}/releases" 2>/dev/null | grep -v '^\.')
}

rollback() {
  local current previous
  current="$(current_release)"
  previous="$(cat "${ROOT}/releases/.previous" 2>/dev/null || true)"
  [[ -n "${previous}" && -d "${ROOT}/releases/${previous}" ]] || fail "没有可以回滚的上一个发布"
  [[ -z "${current}" ]] || "${ROOT}/releases/${current}/stop.sh" || true
  switch_current "${previous}"
  printf '%s\n' "${current}" >"${ROOT}/releases/.previous"
  echo "已切换到发布 ${previous}"
  "${ROOT}/current/start.sh"
}

main() {
  mkdir -p "${ROOT}/releases" "${ROOT}/data" "${ROOT}/logs" "${ROOT}/run/paused" "${ROOT}/secrets" "${ROOT}/certs"
  chmod 0700 "${ROOT}/secrets"
  acquire_lock
  if [[ "${ROLLBACK}" == 1 ]]; then
    rollback
    return
  fi
  install_cron
  local previous staged release
  previous="$(current_release)"
  staged="${ROOT}/releases/.${RELEASE_ID}.tmp"
  release="${ROOT}/releases/${RELEASE_ID}"
  [[ "${previous}" != "${RELEASE_ID}" ]] || fail "发布 ${RELEASE_ID} 已是当前发布"
  rm -rf "${staged}" "${release}"
  mkdir -p "${staged}"
  tar -xzf "${ARCHIVE}" -C "${staged}"
  [[ -r "${staged}/runtime/components" ]] || fail "发布包缺少 runtime/components"
  reuse_binaries "${staged}" "${previous}"
  install_incoming "${staged}"
  local name
  for name in ${GENERATE//,/ }; do
    generate_secret "${name}"
  done
  mv "${staged}" "${release}"
  chmod 0755 "${release}"/*.sh "${release}"/lib/*.sh

  local -a selected=() removed=()
  if [[ -n "${COMPONENTS}" ]]; then
    IFS=',' read -r -a selected <<<"${COMPONENTS}"
  else
    mapfile -t selected <"${release}/runtime/components"
    if [[ -n "${previous}" ]]; then
      while IFS= read -r name; do
        grep -qx -- "${name}" "${release}/runtime/components" || removed+=("${name}")
      done <"${ROOT}/releases/${previous}/runtime/components"
    fi
  fi
  # 停止要替换的组件和不再部署在这台主机上的组件。
  if [[ ${#removed[@]} -gt 0 ]]; then
    "${ROOT}/releases/${previous}/stop.sh" "${removed[@]}"
  fi
  "${release}/stop.sh" "${selected[@]}"
  [[ -z "${previous}" ]] || printf '%s\n' "${previous}" >"${ROOT}/releases/.previous"
  switch_current "${RELEASE_ID}"
  write_wrappers
  echo "已切换到发布 ${RELEASE_ID}"
  if [[ "${NO_START}" == 1 ]]; then
    prune_releases "${RELEASE_ID}" "${previous}"
    return 0
  fi
  if ! "${release}/start.sh" "${selected[@]}"; then
    if [[ -n "${previous}" ]]; then
      echo "install: 启动失败，切回发布 ${previous}" >&2
      "${release}/stop.sh" "${selected[@]}" || true
      switch_current "${previous}"
      local -a restart=()
      for name in "${selected[@]}"; do
        grep -qx -- "${name}" "${ROOT}/releases/${previous}/runtime/components" && restart+=("${name}")
      done
      [[ ${#restart[@]} -eq 0 ]] || "${ROOT}/releases/${previous}/start.sh" "${restart[@]}" || true
      rm -f "${ROOT}/releases/.previous"
    fi
    exit 1
  fi
  prune_releases "${RELEASE_ID}" "${previous}"
  "${release}/status.sh" "${selected[@]}" || true
}

main
