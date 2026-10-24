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
RETIRED = "已停用或移除，不再检查"

def check(check_id, enabled, source="observability", group="business"):
    db.execute("INSERT INTO t_monitor_checks (c_space_id, c_check_id, c_name, c_group_name, c_kind, c_enabled, c_source) "
               "VALUES ('crypto', ?, ?, ?, 'external', ?, ?)", (check_id, check_id, group, enabled, source))

def result(check_id, message, ago=0):
    stamp = (now - datetime.timedelta(minutes=ago)).isoformat()
    db.execute("INSERT INTO t_monitor_check_results (c_result_id, c_space_id, c_check_id, c_instance_id, c_success, c_status, c_error_message, c_checked_at) "
               "VALUES (?, 'crypto', ?, 'monitor', 1, 'healthy', ?, ?)", (f"{check_id}-{ago}", check_id, message, stamp))

check("dataset:firing:1m", 1)
result("dataset:firing:1m", "数据落后 3 根")
check("dataset:gone:1m", 1)
result("dataset:gone:1m", RETIRED)
check("deployment:control:admin", 1, "deployment", "components")
db.execute("INSERT INTO t_monitor_alert_rules (c_space_id, c_rule_id, c_check_id, c_failure_threshold, c_success_threshold, c_enabled, c_description) "
           "VALUES ('crypto', 'default:dataset:firing:1m', 'dataset:firing:1m', 1, 1, 1, 'Default MooX monitoring notification')")
db.execute("INSERT INTO t_monitor_alert_rules (c_space_id, c_rule_id, c_check_id, c_failure_threshold, c_success_threshold, c_enabled, c_description) "
           "VALUES ('crypto', 'default:dataset:orphan:1m', 'dataset:orphan:1m', 1, 1, 1, 'Default MooX monitoring notification')")
db.execute("INSERT INTO t_monitor_alert_rules (c_space_id, c_rule_id, c_check_id, c_failure_threshold, c_success_threshold, c_enabled, c_description) "
           "VALUES ('mooxsys', 'default:host:abcd:cpu', 'host:abcd:cpu', 3, 1, 1, '{\"threshold\":80}')")
db.execute("INSERT INTO t_monitor_alert_states (c_space_id, c_rule_id, c_check_id, c_status, c_triggered_at) "
           "VALUES ('crypto', 'default:dataset:firing:1m', 'dataset:firing:1m', 'firing', ?)", (old,))
db.execute("INSERT INTO t_monitor_alert_states (c_space_id, c_rule_id, c_check_id, c_status) "
           "VALUES ('crypto', 'default:other:1m', 'other:1m', 'ok')")
label = '{"view_id":"view_quiet_kline_1h"}'
db.execute("INSERT INTO t_monitor_metric_series (c_service_name, c_instance_id, c_series_id, c_metric_name, c_metric_type, c_labels_json, c_last_seen_at) "
           "VALUES ('storage-view', 'sv@control', 's1', 'moox_storage_view_output_watermark_timestamp_seconds', 'gauge', ?, ?)", (label, old))
fresh = '{"view_id":"view_live_kline_1m"}'
db.execute("INSERT INTO t_monitor_metric_series (c_service_name, c_instance_id, c_series_id, c_metric_name, c_metric_type, c_labels_json, c_last_seen_at) "
           "VALUES ('storage-view', 'sv@control', 's2', 'moox_storage_view_output_watermark_timestamp_seconds', 'gauge', ?, ?)", (fresh, now.isoformat()))
db.commit()
db.close()
EOF
printf '%s\n' '2026-10-24 10:35:52.437	INFO	monitor_stale_checks_pruned count=1 checks=[dataset:old:1m]' 'other line' >"${LOG}"

cat >"${TMP}/env" <<EOF
MOOX_RELEASE_HOST_CONTROL=ubuntu@control.example.com
EOF
export MOOX_RELEASE_ENV="${TMP}/env"
export PATH="${STUBS}:${PATH}"
export MOOX_MONITOR_DB="${DB}" MOOX_MONITOR_LOG="${LOG}"
before="$(shasum "${DB}" | cut -d' ' -f1)"

out="$("${SCRIPT}" --host control)" || fail "脚本执行失败：${out}"
grep -q "default:dataset:firing:1m \[firing\]" <<<"${out}" || fail "缺少未恢复告警：${out}"
grep -q "最近一次检查：数据落后 3 根" <<<"${out}" || fail "缺少最近一次检查结果：${out}"
grep -q "default:other:1m" <<<"${out}" && fail "ok 状态的告警不该列出"
grep -q "启用 · observability · business：2" <<<"${out}" || fail "检查概况不对：${out}"
grep -q -- "- dataset:gone:1m" <<<"${out}" || fail "缺少失效但未清理的检查：${out}"
grep -q -- "- default:dataset:orphan:1m" <<<"${out}" || fail "缺少孤儿规则：${out}"
grep -q "default:host:abcd:cpu" <<<"${out}" && fail "主机规则不该算孤儿"
grep -q "view_quiet_kline_1h" <<<"${out}" || fail "缺少静默视图序列：${out}"
grep -q "view_live_kline_1m" <<<"${out}" && fail "刚上报的视图不该列为静默"
grep -q "monitor_stale_checks_pruned count=1" <<<"${out}" || fail "缺少清理日志：${out}"
[[ "$(shasum "${DB}" | cut -d' ' -f1)" == "${before}" ]] || fail "数据库被修改了"

# 参数校验：未知主机别名、未配置、路径带危险字符。
if out="$("${SCRIPT}" --host nope 2>&1)"; then fail "未配置的主机应报错"; fi
grep -q "MOOX_RELEASE_HOST_NOPE" <<<"${out}" || fail "错误信息应指出缺少的变量：${out}"
if out="$(MOOX_MONITOR_DB="/tmp/x;rm -rf /" "${SCRIPT}" 2>&1)"; then fail "危险路径应报错"; fi
grep -q "绝对路径" <<<"${out}" || fail "危险路径的报错不对：${out}"

echo "OK"
