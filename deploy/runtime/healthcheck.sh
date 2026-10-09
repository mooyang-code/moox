#!/usr/bin/env bash
# 每分钟由 cron 运行：维护锁被占用时整体跳过；否则滚动本地日志，拉起停止或连续探测失败的组件（暂停的组件不管）。
set -euo pipefail
# shellcheck source=lib/runtime.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)/lib/runtime.sh"

mkdir -p "${ROOT}/logs"
LOG_FILE="${ROOT}/logs/healthcheck.log"

check_all() {
  local failed=0 name
  "${RELEASE}/lib/log-rotate.sh" --root "${ROOT}" \
    --max-size-mb "${LOCAL_LOG_MAX_SIZE_MB}" --backup-count "${LOCAL_LOG_BACKUP_COUNT}" || {
    runtime_log "本地日志滚动失败"
    failed=1
  }
  while IFS= read -r name; do
    ensure_component "${name}" || failed=1
  done < <(runtime_components)
  return "${failed}"
}

status=0
with_maintenance_lock 0 check_all >>"${LOG_FILE}" 2>&1 || status=$?
# 维护锁被占用（部署或停机切换中）时跳过本轮。
[[ "${status}" -eq 75 ]] && exit 0
exit "${status}"
