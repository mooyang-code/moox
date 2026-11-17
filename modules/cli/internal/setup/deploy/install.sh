#!/usr/bin/env bash
# MooX 目标机安装脚本：由 moox-cli setup deploy 经 SSH 从标准输入送来执行，不落盘。
#
#   bash -s -- prepare  <部署根> <组件>...       建目录
#   bash -s -- binaries <部署根> <包>            把这次要用的二进制放进 bin/（包里的 bin/，以及 packages.list 列出的已下载组件包）
#   bash -s -- apply    <部署根> <包>            换配置、写秘密文件与单元文件，按启动顺序重启有变化的组件，写 version.json
#
# <包> 是 moox-cli 上传到部署根下的 .deploy-*.tar.gz，用完就删。binaries、apply 持 <部署根>/.deploy.lock，同一台主机不会同时装两次。
# 组件都是 systemd 用户服务 moox-<组件>.service；上次装好之后被停用（disable）的组件只装文件、不启动。
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
# 配置按组件整目录替换；秘密文件逐个写，目标机自己生成的（admin-master.key、pki/、seaweedfs/）不碰。
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
systemctl_user daemon-reload

# ---------------------------------------------------------------- 内置对象存储（SeaweedFS）
# 端口固定、只听 127.0.0.1。访问密钥只在本机生成一次，之后一直沿用；s3.json 与归档 worker 的凭据都由它派生。
sw_master=11470
sw_filer=11472
sw_s3=11473
sw_bucket=moox-archive
sw_prefix=moox-archive/

seaweedfs_prepare() {
  local dir="${root}/secrets/seaweedfs" keys access secret
  mkdir -p "${dir}"
  keys="${dir}/s3-credentials.env"
  if [[ ! -s "${keys}" ]]; then
    printf 'ACCESS_KEY_ID=MOOX%s\nSECRET_ACCESS_KEY=%s\n' \
      "$(od -An -tx1 -N8 /dev/urandom | tr -d ' \n' | tr 'a-f' 'A-F')" "$(od -An -tx1 -N20 /dev/urandom | tr -d ' \n')" >"${keys}.new"
    mv -f "${keys}.new" "${keys}"
  fi
  access="$(sed -n 's/^ACCESS_KEY_ID=//p' "${keys}")"
  secret="$(sed -n 's/^SECRET_ACCESS_KEY=//p' "${keys}")"
  [[ "${access}" =~ ^[A-Za-z0-9]+$ && "${secret}" =~ ^[A-Za-z0-9]+$ ]] || fail "内置对象存储的密钥文件 ${keys} 不合法"
  # SeaweedFS 的简单动作 Write 含删除，所以身份不给动作、只挂策略：在前缀下读写、列举桶，不能删除。
  cat >"${dir}/s3.json.new" <<EOF
{
  "identities": [
    {
      "name": "moox-archive-worker",
      "credentials": [{"accessKey": "${access}", "secretKey": "${secret}"}],
      "actions": [],
      "policy_names": ["moox-archive-writer"]
    }
  ],
  "policies": [
    {
      "name": "moox-archive-writer",
      "content": "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Action\":[\"s3:PutObject\",\"s3:GetObject\"],\"Resource\":[\"arn:aws:s3:::${sw_bucket}/${sw_prefix}*\"]},{\"Effect\":\"Allow\",\"Action\":[\"s3:ListBucket\"],\"Resource\":[\"arn:aws:s3:::${sw_bucket}\"]}]}"
    }
  ]
}
EOF
  mv -f "${dir}/s3.json.new" "${dir}/s3.json"
  place "${keys}" "${root}/secrets/archive-object-store.env" 0600
}

# weed shell 的退出码不可信、master 连不上会卡住：外层限时，按输出文本判断。
sw_shell() {
  printf '%s\n' "$1" | timeout 20 "${root}/bin/weed" -logtostderr=true shell -master="127.0.0.1:${sw_master}" -filer="127.0.0.1:${sw_filer}" 2>&1 || true
}

seaweedfs_ready() {
  local ready=0 out
  for _ in $(seq 1 45); do
    if [[ -n "$(ss -ltnH "sport = :${sw_s3}" 2>/dev/null)" ]] && sw_shell cluster.check | grep -q 'volume server .* \.\.\. ok'; then
      ready=1
      break
    fi
    sleep 2
  done
  if [[ "${ready}" -ne 1 ]]; then
    journalctl --user -u moox-seaweedfs.service -n 30 --no-pager >&2 || true
    fail "内置对象存储没有就绪"
  fi
  if ! sw_shell s3.bucket.list | grep -Eq "^[[:space:]]*${sw_bucket}[[:space:]]"; then
    out="$(sw_shell "s3.bucket.create -name ${sw_bucket}")"
    if ! grep -Eq 'already exists' <<<"${out}" && ! sw_shell s3.bucket.list | grep -Eq "^[[:space:]]*${sw_bucket}[[:space:]]"; then
      fail "建不了桶 ${sw_bucket}：${out}"
    fi
  fi
}

# Storage 主库启动前建好元数据库（已有时只补缺的表）。
storage_init() {
  (cd "${root}/data/storage-primary" && env -i PATH=/usr/bin:/bin HOME="${HOME}" \
    MOOX_STORAGE_ROLE=primary MOOX_STORAGE_HOME="${root}/data/storage-primary" \
    MOOX_STORAGE_CONFIG="${root}/config/storage-primary/storage.yaml" \
    MOOX_STORAGE_METADATA_PATH="${root}/data/storage-primary/metadata/storage_metadata.db" \
    "${root}/bin/moox-storage-cli" init --storage-conf "${root}/config/storage-primary/storage.yaml" \
    --schema-path "${root}/config/storage-primary/metadata.sql" >/dev/null) || fail "Storage 元数据库初始化失败"
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
# components.list 每行：<组件> <是否有变化 0|1> <上次部署记录里有没有它 0|1>，已按启动顺序排好。
while read -r component changed known; do
  [[ -n "${component}" ]] || continue
  unit="moox-${component}.service"
  [[ -f "${units_dir}/${unit}" ]] || fail "缺单元文件 ${unit}"
  mkdir -p "${root}/data/${component}" "${root}/logs/${component}"
  [[ "${component}" != "seaweedfs" ]] || seaweedfs_prepare
  # 上次已经装好、之后被停用（systemctl --user disable）的组件保持停用：只装文件。上次没装完的不算，照常启用。
  if [[ "${known}" == "1" && "$(systemctl_user is-enabled "${unit}" 2>/dev/null || true)" == "disabled" ]]; then
    echo "component ${component} paused"
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
  [[ "${component}" != "seaweedfs" ]] || seaweedfs_ready
done <"${stage}/components.list"

place "${stage}/version.json" "${root}/version.json" 0600
