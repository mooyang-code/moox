#!/usr/bin/env bash
# 腾讯云轻量防火墙脚本的保护：内部端口不能对任意来源开放。
set -euo pipefail
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
SCRIPT="${ROOT}/skills/moox/scripts/tencent_lighthouse_firewall.py"

# 对外端口对任意来源开放：通过。
python3 "${SCRIPT}" add --instance-id lhins-test --ports 11004 --dry-run >/dev/null

# 内部端口配默认的 0.0.0.0/0：拒绝，并且提示要收窄来源。
if output="$(python3 "${SCRIPT}" add --instance-id lhins-test --ports 11003 --dry-run 2>&1)"; then
  echo "内部端口 11003 对任意来源开放没有被拒绝" >&2
  exit 1
fi
grep -q "不能对任意来源" <<<"${output}" || { echo "拒绝信息不对：${output}" >&2; exit 1; }

# ALL 同样拒绝。
if python3 "${SCRIPT}" add --instance-id lhins-test --ports ALL --dry-run >/dev/null 2>&1; then
  echo "ALL 对任意来源开放没有被拒绝" >&2
  exit 1
fi

# 收窄到具体地址之后，内部端口可以开放。
python3 "${SCRIPT}" add --instance-id lhins-test --ports 11003 --cidr 10.0.0.5/32 --dry-run >/dev/null

echo "防火墙脚本保护检查通过"
