#!/usr/bin/env bash
# MooX 目标机安装脚本：由 moox-cli setup deploy 经 SSH 从标准输入送来执行，不落盘。
#
#   bash -s -- prepare  <部署根> <组件>...       建目录
#   bash -s -- binaries <部署根> <包>            把这次要用的二进制放进 bin/（包里的 bin/，以及 packages.list 列出的已下载组件包）
#   bash -s -- apply    <部署根> <包>            换配置、写秘密文件与单元文件，按启动顺序重启有变化的组件，写 version.json
#
# <包> 是 moox-cli 上传到部署根下的 .deploy-*.tar.gz，用完就删。binaries、apply 持 <部署根>/.deploy.lock，同一台主机不会同时装两次。
# 组件都是 systemd 用户服务 moox-<组件>.service；moox.toml 声明停用的组件只装文件，单元停掉且不开机自启。
set -euo pipefail
umask 077

fail() { echo "install: $*" >&2; exit 1; }

mode="${1:-}"
root="${2:-}"
[[ "${root}" = /* && "${root}" != "/" && "${root}" != *$'\n'* && "${root}" != *" "* ]] || fail "部署根必须是不含空格的绝对路径"
units_dir="${HOME}/.config/systemd/user"

if [[ "${mode}" == "prepare" ]]; then
  shift 2
  mkdir -p "${root}" 2>/dev/null || fail "建不了部署根 ${root}：先让当前用户能写它的上级目录（例如 sudo install -d -o $(id -un) ${root}）"
  [[ -O "${root}" && ! -L "${root}" ]] || fail "部署根 ${root} 要属于当前用户 $(id -un)，且不是符号链接"
  chmod 0700 "${root}"
  mkdir -p "${root}/bin" "${root}/config" "${root}/data" "${root}/logs" "${root}/secrets/env" "${root}/secrets/eventbus" "${units_dir}"
  chmod 0755 "${root}/bin"
  for component in "$@"; do
    [[ "${component}" =~ ^[a-z][a-z0-9-]*$ ]] || fail "组件名不合法：${component}"
    mkdir -p "${root}/data/${component}" "${root}/logs/${component}"
  done
  exit 0
fi

pack="${3:-}"
[[ "${mode}" == "binaries" || "${mode}" == "apply" ]] || fail "未知的步骤：${mode}"
[[ "${pack}" == "${root}"/.deploy-*.tar.gz && -f "${pack}" ]] || fail "没有找到上传的部署包"
stage="${pack%.tar.gz}"
cleanup() { rm -rf -- "${stage}" "${pack}"; }
trap cleanup EXIT

exec 9>"${root}/.deploy.lock"
flock -w 600 9 || fail "这台主机上另一次部署还没结束"

rm -rf -- "${stage}"
mkdir -m 0700 "${stage}"
tar -xzf "${pack}" -C "${stage}" --no-same-owner

# 把一个文件装到位：先写 .new 再改名，正在运行的进程不受影响。
place() { # 源 目标 权限
  install -m "$3" "$1" "$2.new"
  mv -f "$2.new" "$2"
}

if [[ "${mode}" == "binaries" ]]; then
  if [[ -f "${stage}/packages.list" ]]; then
    while IFS= read -r package; do
      [[ -n "${package}" ]] || continue
      [[ "${package}" == "${root}/"* && -f "${package}" ]] || fail "组件包不在部署根里：${package}"
      tar -xzf "${package}" -C "${stage}" --no-same-owner bin
    done <"${stage}/packages.list"
  fi
  if [[ -d "${stage}/bin" ]]; then
    for binary in "${stage}/bin"/*; do
      [[ -f "${binary}" ]] || continue
      place "${binary}" "${root}/bin/$(basename "${binary}")" 0755
    done
  fi
  exit 0
fi

systemctl_user() { systemctl --user "$@"; }

# ---------------------------------------------------------------- 配置、秘密文件、单元文件
# 配置按组件整目录替换；秘密文件逐个写，目标机自己生成的（admin-master.key、pki/）不碰。
if [[ -d "${stage}/config" ]]; then
  for directory in "${stage}/config"/*; do
    [[ -d "${directory}" ]] || continue
    component="$(basename "${directory}")"
    rm -rf -- "${root}/config/${component}.new"
    cp -R "${directory}" "${root}/config/${component}.new"
    rm -rf -- "${root}/config/${component}"
    mv "${root}/config/${component}.new" "${root}/config/${component}"
  done
fi
if [[ -d "${stage}/secrets" ]]; then
  (cd "${stage}/secrets" && find . -type f -print0) | while IFS= read -r -d '' file; do
    mkdir -p "${root}/secrets/$(dirname "${file}")"
    place "${stage}/secrets/${file}" "${root}/secrets/${file}" 0600
  done
fi
if [[ -d "${stage}/units" ]]; then
  for unit in "${stage}/units"/moox-*.service; do
    [[ -f "${unit}" ]] || continue
    place "${unit}" "${units_dir}/$(basename "${unit}")" 0644
  done
fi

# 整台部署时，停掉并删除不再属于这台主机的 moox-* 单元（数据目录留着）。
if [[ -f "${stage}/units.keep" ]]; then
  for unit in "${units_dir}"/moox-*.service; do
    [[ -f "${unit}" ]] || continue
    name="$(basename "${unit}")"
    if ! grep -qxF "${name}" "${stage}/units.keep"; then
      systemctl_user disable --now "${name}" >/dev/null 2>&1 || true
      rm -f -- "${unit}"
      echo "removed ${name%.service}"
    fi
  done
fi
# 历史残留清理：早期版本在因子机随 archive-worker 安装过内置 moox-seaweedfs.service，重部署时自动停用并删掉旧单元。
cleanup_legacy_seaweedfs() {
  local legacy_unit="moox-seaweedfs.service"
  if [[ -f "${units_dir}/${legacy_unit}" ]] || systemctl_user is-active --quiet "${legacy_unit}" >/dev/null 2>&1; then
    systemctl_user disable --now "${legacy_unit}" >/dev/null 2>&1 || true
    rm -f -- "${units_dir}/${legacy_unit}"
    echo "removed moox-seaweedfs"
  fi
}

cleanup_legacy_seaweedfs
systemctl_user daemon-reload

# Storage 主库启动前建好元数据库（已有时只补缺的表）。
storage_init() {
  (cd "${root}/data/storage-primary" && env -i PATH=/usr/bin:/bin HOME="${HOME}" \
    MOOX_STORAGE_ROLE=primary MOOX_STORAGE_HOME="${root}/data/storage-primary" \
    MOOX_STORAGE_CONFIG="${root}/config/storage-primary/storage.yaml" \
    MOOX_STORAGE_METADATA_PATH="${root}/data/storage-primary/metadata/storage_metadata.db" \
    "${root}/bin/moox-storage-cli" init --storage-conf "${root}/config/storage-primary/storage.yaml" >/dev/null) || fail "Storage 元数据库初始化失败"
}

# 重启正在运行的主机网关之后，等它的本机入口重新开始监听再重启后面的组件：它们启动时要从这里取目录，取不到就退出、等 systemd 再拉起。
# 最多等 30 秒，到时限不算失败（部署最后统一等各组件就绪）。首次启动不等：控制机的网关要等后面的 Admin 起来才能就绪。
gateway_wait() {
  local addr
  addr="$(sed -n 's/^[[:space:]]*local_addr:[[:space:]]*\([^[:space:]#]*\).*/\1/p' "${root}/config/host-gateway/app.yaml" 2>/dev/null | head -n 1 || true)"
  [[ "${addr}" =~ ^[0-9.]+:[0-9]+$ ]] || return 0
  for _ in $(seq 1 30); do
    if (exec 3<>"/dev/tcp/${addr%:*}/${addr##*:}") 2>/dev/null; then return 0; fi
    sleep 1
  done
  return 0
}

# ---------------------------------------------------------------- 按启动顺序启动
# components.list 每行：<组件> <是否有变化 0|1> <moox.toml 声明启用 0|1>，已按启动顺序排好。
while read -r component changed wanted; do
  [[ -n "${component}" ]] || continue
  unit="moox-${component}.service"
  [[ -f "${units_dir}/${unit}" ]] || fail "缺单元文件 ${unit}"
  mkdir -p "${root}/data/${component}" "${root}/logs/${component}"
  # 声明停用的组件只装文件：停掉单元、不开机自启。声明启用的照常启用，setup stop 临时停掉的也在这里重新拉起。
  if [[ "${wanted}" != "1" ]]; then
    systemctl_user disable --now "${unit}" >/dev/null 2>&1 || true
    echo "component ${component} disabled"
    continue
  fi
  active=0
  if systemctl_user is-active --quiet "${unit}" >/dev/null 2>&1; then active=1; fi
  if [[ "${changed}" == "1" || "${active}" -eq 0 ]]; then
    [[ "${component}" != "storage-primary" ]] || storage_init
    systemctl_user enable "${unit}" >/dev/null 2>&1 || fail "启用 ${unit} 失败"
    systemctl_user restart "${unit}" || fail "启动 ${unit} 失败：journalctl --user -u ${unit} -n 50"
    if [[ "${active}" -eq 1 ]]; then echo "component ${component} restarted"; else echo "component ${component} started"; fi
    [[ "${component}" != "host-gateway" || "${active}" -eq 0 ]] || gateway_wait
  else
    systemctl_user enable "${unit}" >/dev/null 2>&1 || true
    echo "component ${component} unchanged"
  fi
done <"${stage}/components.list"

place "${stage}/version.json" "${root}/version.json" 0600
