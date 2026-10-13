#!/usr/bin/env bash
# 增量更换 Storage 主机上的 storage-primary（runtime 托管布局）。
#
# runtime 直接 exec 二进制，启动不会执行元数据 init，所以 schema 变更必须在换二进制前
# 手工 init；本脚本把「备份元数据库 → 更新 schema → init → 换二进制 → 重启」串成一步。
#
# 用法: swap-storage-primary.sh <ssh 目标> <二进制目录>
#   二进制目录需包含 moox-storage-primary 与 moox-storage-cli（均为 linux/amd64）
# 环境变量:
#   MOOX_STORAGE_ROOT  远端部署根目录，默认 /data/moox/storage/current
set -euo pipefail

if [[ $# -ne 2 ]]; then
  sed -n '2,10p' "$0" >&2
  exit 2
fi
target="$1"
bin_dir="$2"
root="${MOOX_STORAGE_ROOT:-/data/moox/storage/current}"
repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
schema="${repo}/modules/storage/schema/metadata.sql"

for f in "${bin_dir}/moox-storage-primary" "${bin_dir}/moox-storage-cli" "${schema}"; do
  [[ -f "${f}" ]] || { echo "缺少文件: ${f}" >&2; exit 1; }
done

echo "上传二进制与 schema 到 ${target}"
scp "${bin_dir}/moox-storage-primary" "${target}:${root}/bin/.moox-storage-primary.tmp"
scp "${bin_dir}/moox-storage-cli" "${target}:${root}/bin/.moox-storage-cli.tmp"
scp "${schema}" "${target}:${root}/storage-primary/schema/.metadata.sql.tmp"

# 注意：不要在这里手工 flock 维护锁，被启动的子进程会继承锁描述符，导致之后 status/restart 卡死。
ssh -o BatchMode=yes "${target}" "ROOT='${root}' bash -s" <<'REMOTE'
set -euo pipefail
cd "${ROOT}"
backup="${HOME}/backups/storage_metadata.db.pre-swap-$(date +%s)"
mkdir -p "$(dirname "${backup}")"
cp storage-primary/var/storage/metadata/storage_metadata.db "${backup}"
echo "元数据库已备份: ${backup}"

chmod 0755 bin/.moox-storage-primary.tmp bin/.moox-storage-cli.tmp
mv -f bin/.moox-storage-cli.tmp bin/moox-storage-cli
mv -f storage-primary/schema/.metadata.sql.tmp storage-primary/schema/metadata.sql

# init 可重复执行（IF NOT EXISTS 重放）；失败则中止，旧 storage-primary 仍在运行
(cd storage-primary && ../bin/moox-storage-cli init \
  --storage-conf=config/storage.yaml --schema-path=schema/metadata.sql)
echo "元数据 init 完成"

mv -f bin/.moox-storage-primary.tmp bin/moox-storage-primary
./restart.sh -components storage-primary
sleep 6
./status.sh | python3 -c "import sys,json; [print(c['id'],c['state'],c['ready']) for c in json.load(sys.stdin)['components'] if c['id']=='storage-primary']"
REMOTE
