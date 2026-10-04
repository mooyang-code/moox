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
for trigger in 'CCN' '云联网' 'CCN 费用' 'SCF 公网' '内网组网' 'private-network' 'storage_private_gateway_host' 'SCF VPC' 'restore-scf-public' 'MOOX_STORAGE_RPC_GATEWAY_TARGET'; do
  grep -Fq "${trigger}" <<<"${description}" || fail "skill description is missing the ${trigger} trigger"
done
grep -Fq '主机与 SCF 通信一律走公网' "${SKILL}" || fail "SKILL.md is missing the public-path iron rule"
grep -Fq '主机与 SCF 通信一律走公网' "${CUSTOM_SETUP}" || fail "custom-setup does not keep hosts and SCF on the public path"

grep -Fq 'storage_private_gateway_host' "${TEMPLATE}" || fail "example is missing storage_private_gateway_host guidance"
if grep -Eq '^[[:space:]]*storage_private_gateway_host[[:space:]]*=' "${TEMPLATE}"; then
  fail "example still assigns storage_private_gateway_host"
fi
grep -Fq '不要把 storage_gateway_host 改成内网 IP' "${TEMPLATE}" || fail "example does not warn against rewriting storage_gateway_host"
grep -Fq 'SCF 统一走公网 Storage RPC' "${TEMPLATE}" || fail "example does not say SCF uses the public Storage RPC"
if grep -Fq '组网完成后把 scf_fetcher.storage_gateway_host' "${TEMPLATE}"; then
  fail "example still tells operators to replace storage_gateway_host with a private IP"
fi
if grep -Fq '国内 SCF 走内网' "${SKILL}" "${REFERENCE}" "${CUSTOM_SETUP}" "${TEMPLATE}"; then
  fail "docs still tell agents to send mainland SCF over CCN"
fi

for required in \
  'moox-cli setup private-network' \
  '--dry-run' \
  '--restore-scf-public' \
  '--update-scf-gateway' \
  '--probe-only' \
  'storage_private_gateway_host' \
  'storage_gateway_host' \
  'ModifyInstancesVpcAttribute' \
  'EventBus' \
  'MOOX_PUBLIC_HOST' \
  'MOOX_EVENTBUS_NATS_URL' \
  'MOOX_STORAGE_RPC_GATEWAY_TARGET' \
  'public_net_status' \
  '不再创建云联网' \
  '主机与 SCF 通信一律走公网' \
  '--rewrite-runtime' \
  './bin/moox-cli setup validate --file ./moox.toml'
do
  grep -Fq -- "${required}" "${REFERENCE}" || fail "reference is missing ${required}"
done

if grep -Eq 'cat moox.toml|source moox.toml' "${REFERENCE}"; then
  fail "reference must not instruct agents to dump moox.toml"
fi

help="$(cd "${ROOT}/modules/cli" && go run ./cmd/moox-cli setup private-network --help)"
for flag in --file --dry-run --restore-scf-public --update-scf-gateway --probe-only --rewrite-runtime --skip-scf --skip-hosts --skip-probe; do
  grep -Fq -- "${flag}" <<<"${help}" || fail "CLI help is missing ${flag}"
done
grep -Fq '不再创建云联网' <<<"${help}" || fail "CLI help still describes private CCN setup"

echo 'PASS: skill private-network contract'
