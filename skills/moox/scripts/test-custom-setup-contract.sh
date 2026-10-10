#!/usr/bin/env bash
# 首次部署技能的契约：参考文档、moox.toml.example、命令顺序和凭据边界保持一致，文档里的命令在 CLI 里真实存在。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SKILL="${ROOT}/skills/moox/SKILL.md"
REFERENCE="${ROOT}/skills/moox/references/custom-setup.md"
TEMPLATE="${ROOT}/moox.toml.example"
CREDENTIALS="${ROOT}/modules/cli/internal/setup/deploy/credentials.go"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

[[ -f "${REFERENCE}" ]] || fail "缺少首次部署参考文档"
for required in \
  'moox.toml.example' \
  'chmod 0600 ./moox.toml' \
  '用户必须在部署前填写' \
  '明文凭据' \
  '保持不变' \
  '[hosts.<主机 ID>]' \
  '[placements]' \
  '[eventbus]' \
  '[notification]' \
  '[compile_host]' \
  '[egress_proxy]' \
  '唯一需要用户提前填写的监控专用信息' \
  'TimeSeries Dataset + Frequency' \
  '--config-dir ./config/setup' \
  'partial import' \
  'host_key_unknown' \
  'login_api: valid' \
  '禁止 Agent'
do
  grep -Fq -- "${required}" "${REFERENCE}" || fail "参考文档缺少 ${required}"
done

# 命令顺序：validate → firewall → bootstrap → apply → status → init。firewall 必须在 bootstrap 之前：
# 其他主机的主机网关要经 control 的 11003 取到第一份快照才算就绪。
line_of() {
  local line
  line="$(grep -nF -- "$1" "${REFERENCE}" | head -1 | cut -d: -f1)"
  [[ -n "${line}" ]] || fail "参考文档缺少命令 $1"
  printf '%s' "${line}"
}
validate_line="$(line_of './bin/moox-cli setup validate --file ./moox.toml')"
firewall_line="$(line_of './bin/moox-cli setup firewall --file ./moox.toml')"
bootstrap_line="$(line_of './bin/moox-cli setup bootstrap --file ./moox.toml')"
apply_line="$(line_of './bin/moox-cli setup apply --file ./moox.toml')"
status_line="$(line_of './bin/moox-cli setup status --file ./moox.toml')"
init_line="$(line_of './bin/moox-cli setup init --file ./moox.toml --config-dir ./config/setup')"
[[ "${validate_line}" -lt "${firewall_line}" && "${firewall_line}" -lt "${bootstrap_line}" &&
  "${bootstrap_line}" -lt "${apply_line}" && "${apply_line}" -lt "${status_line}" && "${status_line}" -lt "${init_line}" ]] ||
  fail "命令顺序必须是 validate → firewall → bootstrap → apply → status → init"

# 凭据边界：这些都是禁止 Agent 做的事，文档里必须点名。
for forbidden in 'cat moox.toml' 'sed moox.toml' 'rg moox.toml' 'Python 读取' 'source moox.toml'; do
  grep -Fq -- "${forbidden}" "${REFERENCE}" || fail "参考文档没有点名禁止 ${forbidden}"
done
grep -Fq -- '返回 `completed`' "${REFERENCE}" || fail "参考文档没有说明 status 返回 completed 之后才能继续"

grep -Fq 'references/custom-setup.md' "${SKILL}" || fail "SKILL.md 没有指向首次部署参考文档"
grep -Fq 'moox-cli setup bootstrap' "${SKILL}" || fail "SKILL.md 没有说明 bootstrap"
# 已删除的命令和脚本不能再出现。
for removed in 'deploy-control' 'deploy-storage' 'caddy-prerequisite.sh' 'deploy-moox.sh' 'control_host' 'other_hosts' 'storage_gateway_host'; do
  if grep -Fq -- "${removed}" "${SKILL}" "${REFERENCE}"; then
    fail "技能文档仍然提到已删除的 ${removed}"
  fi
done

# 示例文件。
for required in '[notification]' 'channel_type = "wecom"' 'webhook_url = ""' '[hosts.control]' '[placements]' '[egress_proxy]' 'private_address' 'region ='; do
  grep -Fq -- "${required}" "${TEMPLATE}" || fail "moox.toml.example 缺少 ${required}"
done
grep -Fq 'notification.env' "${CREDENTIALS}" || fail "部署工具没有把告警 webhook 写进 secrets/notification.env"

# 文档里的命令在 CLI 里必须真实存在。
help="$(cd "${ROOT}/modules/cli" && go run ./cmd/moox-cli setup --help)"
for command in validate trust-host trust-browser firewall bootstrap apply status init hosts; do
  grep -Eq "^  ${command} " <<<"${help}" || fail "moox-cli setup 没有 ${command} 子命令"
done

echo 'custom setup Skill contract passed'
