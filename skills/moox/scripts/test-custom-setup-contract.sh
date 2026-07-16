#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SKILL="${ROOT}/skills/moox/SKILL.md"
REFERENCE="${ROOT}/skills/moox/references/custom-setup.md"
TEMPLATE="${ROOT}/custom.toml.example"

[[ -f "${REFERENCE}" ]] || { echo 'missing custom setup reference' >&2; exit 1; }
grep -Fq 'custom.toml.example' "${REFERENCE}"
grep -Fq 'chmod 0600 ./custom.toml' "${REFERENCE}"
grep -Fq '用户必须在部署前填写' "${REFERENCE}"
grep -Fq '明文凭据' "${REFERENCE}"
grep -Fq '保持不变' "${REFERENCE}"

validate_line=$(grep -nF './bin/moox-cli setup validate --file ./custom.toml' "${REFERENCE}" | cut -d: -f1)
deploy_line=$(grep -nF './bin/moox-cli setup deploy-control --file ./custom.toml' "${REFERENCE}" | cut -d: -f1)
apply_line=$(grep -nF './bin/moox-cli setup apply --file ./custom.toml' "${REFERENCE}" | cut -d: -f1)
status_line=$(grep -nF './bin/moox-cli setup status --file ./custom.toml' "${REFERENCE}" | cut -d: -f1)
[[ "${validate_line}" -lt "${deploy_line}" && "${deploy_line}" -lt "${apply_line}" && "${apply_line}" -lt "${status_line}" ]]

grep -Fq '禁止 Agent' "${REFERENCE}"
for forbidden in 'cat custom.toml' 'sed custom.toml' 'rg custom.toml' 'Python 读取' 'source custom.toml'; do
  grep -Fq "${forbidden}" "${REFERENCE}"
done
grep -Fq 'status 返回 completed 后' "${REFERENCE}"
grep -Fq 'references/custom-setup.md' "${SKILL}"

template_block=$(awk '/^```toml$/{inside=1; next} inside && /^```$/{exit} inside{print}' "${REFERENCE}")
cmp -s <(printf '%s\n' "${template_block}") "${TEMPLATE}" || { echo 'reference template differs from custom.toml.example' >&2; exit 1; }

echo 'custom setup Skill contract passed'
