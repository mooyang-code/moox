#!/usr/bin/env bash
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

[[ -f "${REFERENCE}" ]] || fail "private network reference is missing"
grep -Fq 'references/private-network.md' "${SKILL}" || fail "SKILL.md does not route private-network to the reference"
grep -Fq '### Tencent Private Network' "${SKILL}" || fail "SKILL.md is missing the private network section"
grep -Fq 'references/private-network.md' "${CUSTOM_SETUP}" || fail "custom-setup does not point agents at private-network.md"

description="$(sed -n 's/^description: //p' "${SKILL}" | head -1)"
for trigger in 'CCN' '云联网' 'SCF 公网' 'SCF VPC' '内网组网' 'private-network' 'MOOX_ACCESS_ADDRESS'; do
  grep -Fq "${trigger}" <<<"${description}" || fail "skill description is missing the ${trigger} trigger"
done

# 示例配置必须说明 SCF 经同地域 Access 私网地址接入。
for required in 'scf_plan.regions' 'access_host' 'private_address'; do
  grep -Fq "${required}" "${TEMPLATE}" || fail "example is missing ${required} guidance"
done

for required in \
  'private_address' \
  'access_host' \
  'scf_plan.regions' \
  'MOOX_ACCESS_ADDRESS' \
  'public_net_status' \
  'ModifyInstancesVpcAttribute' \
  'MOOX_PUBLIC_HOST' \
  'EventBus' \
  'setup inspect-scf' \
  './bin/moox-cli setup validate --file ./moox.toml'
do
  grep -Fq -- "${required}" "${REFERENCE}" || fail "reference is missing ${required}"
done

if grep -Eq 'cat moox.toml|source moox.toml' "${REFERENCE}"; then
  fail "reference must not instruct agents to dump moox.toml"
fi

echo 'PASS: skill private-network contract'
