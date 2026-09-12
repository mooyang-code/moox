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
for trigger in 'CCN' '云联网' '内网组网' 'private-network' 'storage_private_gateway_host' 'SCF VPC'; do
  grep -Fq "${trigger}" <<<"${description}" || fail "skill description is missing the ${trigger} trigger"
done

grep -Fq 'storage_private_gateway_host' "${TEMPLATE}" || fail "example is missing storage_private_gateway_host"
if grep -Eq '^[[:space:]]*#[[:space:]]*storage_private_gateway_host[[:space:]]*=' "${TEMPLATE}"; then
  fail "example still comments out storage_private_gateway_host"
fi
grep -Fq '不要把 storage_gateway_host 改成内网 IP' "${TEMPLATE}" || fail "example does not warn against rewriting storage_gateway_host"
if grep -Fq '组网完成后把 scf_fetcher.storage_gateway_host' "${TEMPLATE}"; then
  fail "example still tells operators to replace storage_gateway_host with a private IP"
fi

for required in \
  'moox-cli setup private-network' \
  '--dry-run' \
  '--update-scf-gateway' \
  '--probe-only' \
  'storage_private_gateway_host' \
  'storage_gateway_host' \
  'ModifyInstancesVpcAttribute' \
  'moox-private-network-global' \
  'EventBus' \
  'MOOX_PUBLIC_HOST' \
  './bin/moox-cli setup validate --file ./moox.toml'
do
  grep -Fq -- "${required}" "${REFERENCE}" || fail "reference is missing ${required}"
done

if grep -Eq 'cat moox.toml|source moox.toml' "${REFERENCE}"; then
  fail "reference must not instruct agents to dump moox.toml"
fi

help="$(cd "${ROOT}/modules/cli" && go run ./cmd/moox-cli setup private-network --help)"
for flag in --file --dry-run --update-scf-gateway --probe-only --rewrite-runtime --skip-scf --skip-hosts --skip-probe; do
  grep -Fq -- "${flag}" <<<"${help}" || fail "CLI help is missing ${flag}"
done

echo 'PASS: skill private-network contract'
