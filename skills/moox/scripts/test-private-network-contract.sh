#!/usr/bin/env bash
# 腾讯云分地域网络技能的契约：参考文档、SKILL.md 触发词、moox.toml.example 和 CLI 帮助保持一致。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SKILL="${ROOT}/skills/moox/SKILL.md"
REFERENCE="${ROOT}/skills/moox/references/private-network.md"
CUSTOM_SETUP="${ROOT}/skills/moox/references/custom-setup.md"
TEMPLATE="${ROOT}/moox.toml.example"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

[[ -f "${REFERENCE}" ]] || fail "缺少私网参考文档"
grep -Fq 'references/private-network.md' "${SKILL}" || fail "SKILL.md 没有把私网问题指向参考文档"
grep -Fq '### 腾讯云 SCF 与外部接入的网络' "${SKILL}" || fail "SKILL.md 缺少 SCF 与外部接入的网络小节"
grep -Fq 'references/private-network.md' "${CUSTOM_SETUP}" || fail "首次部署文档没有指向 private-network.md"

description="$(sed -n 's/^description: //p' "${SKILL}" | head -1)"
for trigger in 'CCN' '云联网' 'CCN 费用' 'SCF 公网' '内网组网' 'private-network' 'SCF VPC' 'scf-network-plan' 'MOOX_ACCESS_ADDRESS'; do
  grep -Fq "${trigger}" <<<"${description}" || fail "SKILL.md 的 description 缺少触发词 ${trigger}"
done

# 外部接入的私网路由只来自主机的 private_address 和 region；旧的 Storage 网关地址配置已经删除。
for required in 'private_address' 'region ='; do
  grep -Fq -- "${required}" "${TEMPLATE}" || fail "moox.toml.example 缺少 ${required}"
done
for removed in 'storage_private_gateway_host' 'storage_gateway_host' 'MOOX_STORAGE_RPC_GATEWAY_TARGET' 'restore-scf-public' '--rewrite-runtime'; do
  if grep -Fq -- "${removed}" "${SKILL}" "${REFERENCE}" "${CUSTOM_SETUP}" "${TEMPLATE}"; then
    fail "文档里仍然提到已删除的 ${removed}"
  fi
done
if grep -Fq '国内 SCF 走内网' "${SKILL}" "${REFERENCE}" "${CUSTOM_SETUP}" "${TEMPLATE}"; then
  fail "文档仍然让 Agent 把国内 SCF 经云联网走内网"
fi

for required in \
  'moox-cli setup scf-network-plan' \
  'moox-cli setup private-network' \
  '--dry-run' \
  '--skip-probe' \
  '--probe-regions' \
  'ModifyInstancesVpcAttribute' \
  '消息总线' \
  '[hosts.control] address' \
  'MOOX_EVENTBUS_NATS_URL' \
  'MOOX_ACCESS_ADDRESS' \
  'public_net_status' \
  '不创建云联网' \
  '11004' \
  './bin/moox-cli setup validate --file ./moox.toml'
do
  grep -Fq -- "${required}" "${REFERENCE}" || fail "参考文档缺少 ${required}"
done

if grep -Eq 'cat moox.toml|source moox.toml' "${REFERENCE}"; then
  fail "参考文档不能让 Agent 读出 moox.toml"
fi

# 文档里的命令和参数在 CLI 里必须真实存在。
help="$(cd "${ROOT}/modules/cli" && go run ./cmd/moox-cli setup private-network --help)"
for flag in --file --dry-run --skip-probe --probe-regions; do
  grep -Fq -- "${flag}" <<<"${help}" || fail "setup private-network 的帮助缺少 ${flag}"
done
grep -Fq '不创建云联网' <<<"${help}" || fail "setup private-network 的帮助没有说明不创建云联网"
plan_help="$(cd "${ROOT}/modules/cli" && go run ./cmd/moox-cli setup scf-network-plan --help)"
for flag in --file --region; do
  grep -Fq -- "${flag}" <<<"${plan_help}" || fail "setup scf-network-plan 的帮助缺少 ${flag}"
done

echo 'PASS: skill private-network contract'
