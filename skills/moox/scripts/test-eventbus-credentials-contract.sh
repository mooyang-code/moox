#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SKILL="${ROOT}/skills/moox/SKILL.md"
REFERENCE="${ROOT}/skills/moox/references/eventbus-credentials.md"
HOST_AGENT="${ROOT}/skills/moox/references/host-agent.md"
SERVICE_RELEASE="${ROOT}/skills/moox/references/service-release.md"
RELEASE="${ROOT}/skills/moox/references/release.md"
DEPLOY="${ROOT}/skills/moox/scripts/hostagent-deploy.sh"

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
  'hostagent-deploy.sh' \
  '--credentials-only' \
  '~/.config/moox/hostagent/eventbus.yaml' \
  'deploy-storage' \
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

grep -Fq -- '--credentials-only' "${HOST_AGENT}" || fail "host-agent.md does not document credentials-only deploy"
grep -Fq 'eventbus-credentials.md' "${HOST_AGENT}" || fail "host-agent.md does not point at EventBus fan-out"
grep -Fq 'eventbus-credentials.md' "${SERVICE_RELEASE}" || fail "service-release.md does not require EventBus fan-out before deploy-service"
grep -Fq 'deploy-service' "${SERVICE_RELEASE}" || fail "service-release.md lost deploy-service"
grep -Fq -- '--credentials-only' "${RELEASE}" || fail "release.md does not mention credentials-only Host Agent sync"
grep -Fq -- '--credentials-only' "${DEPLOY}" || fail "hostagent-deploy.sh is missing --credentials-only"

echo 'PASS: skill eventbus credentials contract'
