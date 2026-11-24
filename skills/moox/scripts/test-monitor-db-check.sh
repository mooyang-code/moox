#!/usr/bin/env bash
# monitor-db-check.sh 的契约测试：用替身 ssh 在本机执行远端命令，数据库用 Monitor 的真实建表脚本加固定的几行数据，
# 核对输出里的各个段落，并确认数据库没有被修改。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SCRIPT="${ROOT}/skills/moox/scripts/monitor-db-check.sh"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/monitor-db-check-test.XXXXXX")"
trap 'rm -rf "${TMP}"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

STUBS="${TMP}/stubs"
mkdir -p "${STUBS}"
cat >"${STUBS}/ssh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
while [[ "${1:-}" == -o ]]; do shift 2; done
shift   # 主机
exec bash -c "$*"
EOF
chmod +x "${STUBS}/ssh"

DB="${TMP}/monitor.db"
LOG="${TMP}/trpc.log"
python3 -I - "${ROOT}/modules/monitor/schema/monitor.sql" "${DB}" <<'EOF'
import datetime
import sqlite3
import sys

schema, path = sys.argv[1], sys.argv[2]
db = sqlite3.connect(path)
db.executescript(open(schema, encoding="utf-8").read())
now = datetime.datetime.now(datetime.timezone.utc)
old = (now - datetime.timedelta(hours=3)).isoformat()

def check(check_id, status, reason, alert="ok", triggered=None, failure=""):
    db.execute("INSERT INTO t_monitor_checks (c_space_id, c_check_id, c_name, c_severity, c_status, c_reason, c_checked_at, "
               "c_failure_reason, c_alert_status, c_triggered_at) VALUES ('crypto', ?, ?, 'critical', ?, ?, ?, ?, ?, ?)",
               (check_id, check_id, status, reason, now.isoformat(), failure, alert, triggered))

check("dataset:storage:firing:1m", "down", "数据落后 3 根", "firing", old, "数据落后 3 根")
check("dataset:storage:recovering:1m", "ok", "正常", "firing", old, "数据落后 5 根")
check("dataset:storage:other:1m", "ok", "正常")
check("host:abcd:cpu", "ok", "CPU 使用率 20.0%")
db.execute("INSERT INTO t_monitor_alert_events (c_event_id, c_space_id, c_check_id, c_title, c_severity, c_event_type, c_created_at) "
           "VALUES ('e1', 'crypto', 'dataset:storage:firing:1m', '行情数据集', 'critical', 'triggered', ?)", (old,))
label = '{"view_id":"view_quiet_kline_1h"}'
db.execute("INSERT INTO t_monitor_metric_series (c_service_name, c_instance_id, c_series_id, c_metric_name, c_metric_type, c_labels_json, c_last_seen_at, c_value, c_observed_at) "
           "VALUES ('storage-view', 'sv@control', 's1', 'moox_storage_view_output_watermark_timestamp_seconds', 'gauge', ?, ?, 0, ?)", (label, old, old))
fresh = '{"view_id":"view_live_kline_1m"}'
db.execute("INSERT INTO t_monitor_metric_series (c_service_name, c_instance_id, c_series_id, c_metric_name, c_metric_type, c_labels_json, c_last_seen_at, c_value, c_observed_at) "
           "VALUES ('storage-view', 'sv@control', 's2', 'moox_storage_view_output_watermark_timestamp_seconds', 'gauge', ?, ?, 0, ?)", (fresh, now.isoformat(), now.isoformat()))
db.commit()
db.close()
EOF
printf '%s\n' '2026-10-24 10:35:52.437	WARN	monitor alert notification partially failed check=dataset:storage:firing:1m' 'other line' >"${LOG}"

export PATH="${STUBS}:${PATH}"
export MOOX_MONITOR_DB="${DB}" MOOX_MONITOR_LOG="${LOG}"
before="$(shasum "${DB}" | cut -d' ' -f1)"

out="$("${SCRIPT}" --host ubuntu@control.example.com)" || fail "脚本执行失败：${out}"
grep -q "crypto/dataset:storage:firing:1m \[critical\]" <<<"${out}" || fail "缺少未恢复告警：${out}"
grep -q "最近一次检查：数据落后 3 根" <<<"${out}" || fail "缺少最近一次检查结果：${out}"
grep -q "等待恢复确认（最近失败：数据落后 5 根）" <<<"${out}" || fail "恢复确认中的告警应显示最近失败：${out}"
grep -q "crypto/dataset:storage:other:1m" <<<"${out}" && fail "没在告警的检查不该列出"
grep -q "dataset · ok：2" <<<"${out}" || fail "检查概况不对：${out}"
grep -q "triggered crypto/dataset:storage:firing:1m（行情数据集）" <<<"${out}" || fail "缺少告警事件：${out}"
grep -q "view_quiet_kline_1h" <<<"${out}" || fail "缺少静默视图序列：${out}"
grep -q "view_live_kline_1m" <<<"${out}" && fail "刚上报的视图不该列为静默"
grep -q "partially failed" <<<"${out}" || fail "缺少推送失败日志：${out}"
[[ "$(shasum "${DB}" | cut -d' ' -f1)" == "${before}" ]] || fail "数据库被修改了"

# 参数校验：没给主机、主机不是 user@host、路径带危险字符。
if out="$("${SCRIPT}" 2>&1)"; then fail "没给主机应报错"; fi
grep -q -- "--host" <<<"${out}" || fail "没给主机时应打印用法：${out}"
if out="$("${SCRIPT}" --host 'control;id' 2>&1)"; then fail "不合法的主机应报错"; fi
grep -q "user@host" <<<"${out}" || fail "主机格式的报错不对：${out}"
if out="$(MOOX_MONITOR_DB="/tmp/x;rm -rf /" "${SCRIPT}" --host ubuntu@control.example.com 2>&1)"; then fail "危险路径应报错"; fi
grep -q "绝对路径" <<<"${out}" || fail "危险路径的报错不对：${out}"

echo "OK"
