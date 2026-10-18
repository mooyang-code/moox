#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SKILL="${ROOT}/skills/moox/SKILL.md"
REFERENCE="${ROOT}/skills/moox/references/eventbus-credentials.md"
HOST_AGENT="${ROOT}/skills/moox/references/host-agent.md"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

[[ -f "${REFERENCE}" ]] || fail "eventbus credentials reference is missing"
grep -Fq 'references/eventbus-credentials.md' "${SKILL}" || fail "SKILL.md does not route EventBus rotation to the reference"
grep -Fq 'Control `export` does not update Host Agents' "${SKILL}" || fail "SKILL.md does not say control export skips remote Host Agents"

description="$(sed -n 's/^description: //p' "${SKILL}" | head -1)"
for trigger in 'EventBus rotate' 'Authorization Violation' 'FIN-WAIT-2' 'certificate signature failure'; do
  grep -Fq "${trigger}" <<<"${description}" || fail "skill description is missing the ${trigger} trigger"
done

for required in \
  'fan-out' \
  'host-agent/config/eventbus.yaml' \
  'setup deploy-host' \
  'MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64' \
  'Authorization Violation' \
  'certificate signature failure' \
  'FIN-WAIT-2' \
  '只换了二进制' \
  'control 已经 export'
do
  grep -Fq -- "${required}" "${REFERENCE}" || fail "reference is missing ${required}"
done

if grep -Eq 'cat moox.toml|source moox.toml' "${REFERENCE}"; then
  fail "reference must not instruct agents to dump moox.toml"
fi
if grep -Eq 'BEGIN CERTIFICATE|NATS_PASSWORD|password:' "${REFERENCE}"; then
  fail "reference must not embed certificates or passwords"
fi

grep -Fq 'setup deploy-host' "${HOST_AGENT}" || fail "host-agent.md does not document reinstalling the host unit"
grep -Fq 'eventbus-credentials.md' "${HOST_AGENT}" || fail "host-agent.md does not point at EventBus fan-out"

echo 'PASS: skill eventbus credentials contract'
