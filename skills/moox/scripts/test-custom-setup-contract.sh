#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SKILL="${ROOT}/skills/moox/SKILL.md"
REFERENCE="${ROOT}/skills/moox/references/custom-setup.md"
TEMPLATE="${ROOT}/moox.toml.example"
DEPLOY="${ROOT}/scripts/deploy/deploy-moox.sh"

[[ -f "${REFERENCE}" ]] || { echo 'missing custom setup reference' >&2; exit 1; }
grep -Fq 'moox.toml.example' "${REFERENCE}"
grep -Fq 'chmod 0600 ./moox.toml' "${REFERENCE}"
grep -Fq '用户必须在部署前填写' "${REFERENCE}"
grep -Fq '明文凭据' "${REFERENCE}"
grep -Fq '保持不变' "${REFERENCE}"
grep -Fq '[eventbus]' "${REFERENCE}"
grep -Fq 'public_address = ""' "${REFERENCE}"
grep -Fq '公网连接事实' "${REFERENCE}"
grep -Fq 'cloudnode-worker.yaml' "${REFERENCE}"
grep -Fq '[notification]' "${REFERENCE}"
grep -Fq 'channel_type = "wecom"' "${REFERENCE}"
grep -Fq 'webhook_url = ""' "${REFERENCE}"
grep -Fq '唯一需要用户提前填写的监控专用信息' "${REFERENCE}"
grep -Fq 'TimeSeries Dataset + Frequency' "${REFERENCE}"
grep -Fq 'MOOX_NOTIFICATION_WEBHOOK_URL=%q' "${DEPLOY}"
grep -Fq 'secrets/notification.env' "${DEPLOY}"

validate_line=$(grep -nF './bin/moox-cli setup validate --file ./moox.toml' "${REFERENCE}" | cut -d: -f1)
deploy_line=$(grep -nF './bin/moox-cli setup deploy-control --file ./moox.toml' "${REFERENCE}" | cut -d: -f1)
apply_line=$(grep -nF './bin/moox-cli setup apply --file ./moox.toml' "${REFERENCE}" | cut -d: -f1)
status_line=$(grep -nF './bin/moox-cli setup status --file ./moox.toml' "${REFERENCE}" | cut -d: -f1)
[[ "${validate_line}" -lt "${deploy_line}" && "${deploy_line}" -lt "${apply_line}" && "${apply_line}" -lt "${status_line}" ]]

hosts_line=$(grep -nF './bin/moox-cli setup hosts --file ./moox.toml' "${REFERENCE}" | cut -d: -f1)
storage_line=$(grep -nF './bin/moox-cli setup deploy-storage --file ./moox.toml --host <host-name>' "${REFERENCE}" | cut -d: -f1)
init_line=$(grep -nF './bin/moox-cli setup init' "${REFERENCE}" | cut -d: -f1)
[[ "${status_line}" -lt "${hosts_line}" && "${hosts_line}" -lt "${storage_line}" && "${storage_line}" -lt "${init_line}" ]]
grep -Fq -- '--config-dir ./config/setup' "${REFERENCE}"
grep -Fq 'partial import' "${REFERENCE}"

grep -Fq '禁止 Agent' "${REFERENCE}"
for forbidden in 'cat moox.toml' 'sed moox.toml' 'rg moox.toml' 'Python 读取' 'source moox.toml'; do
  grep -Fq "${forbidden}" "${REFERENCE}"
done
grep -Fq 'status 返回 `completed`' "${REFERENCE}"
grep -Fq 'references/custom-setup.md' "${SKILL}"
grep -Fq 'owns the Caddy prerequisite' "${SKILL}"
grep -Fq 'certificate` summary' "${SKILL}"
if grep -Fq 'caddy-prerequisite.sh ensure' "${SKILL}"; then
  echo 'initialization Skill must use moox-cli for the Caddy prerequisite' >&2
  exit 1
fi

grep -Fq '[notification]' "${TEMPLATE}"
grep -Fq 'channel_type = "wecom"' "${TEMPLATE}"
grep -Fq 'webhook_url = ""' "${TEMPLATE}"

echo 'custom setup Skill contract passed'
