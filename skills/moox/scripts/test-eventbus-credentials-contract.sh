#!/usr/bin/env bash
# EventBus 凭据同步技能的契约：fan-out 清单、触发词和各文档之间的指向保持一致，文档里的 role 文件名在代码里真实存在。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SKILL="${ROOT}/skills/moox/SKILL.md"
REFERENCE="${ROOT}/skills/moox/references/eventbus-credentials.md"
HOST_AGENT="${ROOT}/skills/moox/references/host-agent.md"
RELEASE="${ROOT}/skills/moox/references/release.md"
ROLE_FILES="${ROOT}/modules/admin/cmd/cli/eventbus_credentials.go"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

[[ -f "${REFERENCE}" ]] || fail "缺少 EventBus 凭据参考文档"
grep -Fq 'references/eventbus-credentials.md' "${SKILL}" || fail "SKILL.md 没有把 EventBus 轮换指向参考文档"
grep -Fq 'export` 不会更新其他主机' "${SKILL}" || fail "SKILL.md 没有说明 control 上的 export 不会更新其他主机"

description="$(sed -n 's/^description: //p' "${SKILL}" | head -1)"
for trigger in 'EventBus rotate' 'Authorization Violation' 'FIN-WAIT-2' 'certificate signature failure'; do
  grep -Fq "${trigger}" <<<"${description}" || fail "SKILL.md 的 description 缺少触发词 ${trigger}"
done

for required in \
  'fan-out' \
  'moox-admin-cli eventbus-credentials' \
  'deploy-host' \
  '--reuse-binaries' \
  'secrets/eventbus' \
  'MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64' \
  'Authorization Violation' \
  'certificate signature failure' \
  'FIN-WAIT-2' \
  '只换了二进制' \
  'control 已经 export'
do
  grep -Fq -- "${required}" "${REFERENCE}" || fail "参考文档缺少 ${required}"
done

if grep -Eq 'cat moox.toml|source moox.toml' "${REFERENCE}"; then
  fail "参考文档不能让 Agent 读出 moox.toml"
fi
if grep -Eq 'BEGIN CERTIFICATE|NATS_PASSWORD|password:' "${REFERENCE}"; then
  fail "参考文档不能内嵌证书或口令"
fi
# 已删除的脚本和参数不能再出现。
for removed in 'hostagent-deploy.sh' 'hostagent-release.sh' '--credentials-only' 'deploy-control' 'deploy-storage' '~/.config/moox/hostagent'; do
  if grep -Fq -- "${removed}" "${SKILL}" "${REFERENCE}" "${HOST_AGENT}" "${RELEASE}"; then
    fail "文档里仍然提到已删除的 ${removed}"
  fi
done

grep -Fq -- '--reuse-binaries' "${HOST_AGENT}" || fail "host-agent.md 没有说明用 --reuse-binaries 同步凭据"
grep -Fq 'eventbus-credentials.md' "${HOST_AGENT}" || fail "host-agent.md 没有指向 EventBus fan-out"
grep -Fq 'eventbus-credentials.md' "${RELEASE}" || fail "release.md 没有要求在 deploy-service 之前完成 EventBus fan-out"
grep -Fq 'deploy-service' "${RELEASE}" || fail "release.md 里没有 deploy-service"
grep -Fq -- '--reuse-binaries' "${RELEASE}" || fail "release.md 没有说明 --reuse-binaries"

# 参考文档里列出的 role 文件名必须和 Admin CLI 导出的一致。
for file in $(grep -Eo '`[a-z-]+\.yaml`' "${REFERENCE}" | tr -d '`' | sort -u); do
  case "${file}" in
    users.yaml) continue ;;
  esac
  grep -Fq "\"${file}\"" "${ROLE_FILES}" || fail "参考文档里的 ${file} 不是 Admin CLI 导出的 role 文件"
done

echo 'PASS: skill eventbus credentials contract'
