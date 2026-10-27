#!/usr/bin/env bash
# strategy_check.py 的契约测试（只依赖 Python 标准库，全部离线）：
# targets / summary 能读策略库，compare 对一致的权重判通过、对不一致判失败、对没有变化的窗口给出警告，
# 截断按十进制向零（0.7284 不会被截成 0.7283），latency / task-lag 能按 bar_end 关联两份日志并算出分段耗时。
set -euo pipefail

SCRIPT_DIR="$(cd "${BASH_SOURCE[0]%/*}" && pwd -P)"
CHECK="${SCRIPT_DIR}/strategy_check.py"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/moox-strategy-check.XXXXXX")"
trap 'rm -rf "${TMP}"' EXIT
fail() { echo "strategy check contract: $*" >&2; exit 1; }

python3 -I - "${TMP}/strategy.sqlite" <<'PY'
import json, sqlite3, sys
c = sqlite3.connect(sys.argv[1])
c.execute("CREATE TABLE t_strategy_results (c_instance_id TEXT, c_bar_end_time INTEGER, c_status TEXT, c_skip_reason TEXT, c_targets_json TEXT, c_rule_states_json TEXT)")
base = 1793037600000  # 2026-10-26T18:00:00Z
rows = [
    ("inst-a", base, "ok", "", [{"instrument_id": "BTC-USDT", "target_weight": "0.5"}], "x" * 100),
    ("inst-a", base + 300000, "ok", "", [{"instrument_id": "BTC-USDT", "target_weight": "0.7284"}, {"instrument_id": "ETH-USDT", "target_weight": "0.1"}], "x" * 200),
    ("inst-a", base + 600000, "ok", "", [{"instrument_id": "ETH-USDT", "target_weight": "0.25"}], "x" * 200),
    ("inst-a", base + 900000, "skipped", "expired", [], "x" * 200),
]
for iid, bar, status, reason, targets, states in rows:
    c.execute("INSERT INTO t_strategy_results VALUES (?, ?, ?, ?, ?, ?)", (iid, bar, status, reason, json.dumps(targets), states))
c.commit()
PY

python3 -I "${CHECK}" targets --db "${TMP}/strategy.sqlite" --instance A=inst-a --output "${TMP}/targets.json" 2>/dev/null
python3 -I -c 'import json,sys; t=json.load(open(sys.argv[1]))["A"]; assert sorted(t)==["2026-10-26T18:00:00Z","2026-10-26T18:05:00Z","2026-10-26T18:10:00Z"], t; assert t["2026-10-26T18:05:00Z"]["BTC-USDT"]=="0.7284"' "${TMP}/targets.json" \
  || fail "targets 应只导出 ok 的期，并保留权重原文"
summary="$(python3 -I "${CHECK}" summary --db "${TMP}/strategy.sqlite" --instance A=inst-a)"
grep -q "跳过原因 {'expired': 1}" <<<"${summary}" || fail "summary 应列出跳过原因：${summary}"
grep -q "最大 200" <<<"${summary}" || fail "summary 应给出状态体积"

cat > "${TMP}/ref.csv" <<'EOF'
bar_end,instrument_id,weight
2026-10-26T18:00:00Z,BTC-USDT,0.50009
2026-10-26T18:05:00Z,BTC-USDT,0.7284
2026-10-26T18:05:00Z,ETH-USDT,0.10004
2026-10-26T18:05:00Z,SOL-USDT,0.00003
2026-10-26T18:10:00Z,ETH-USDT,0.25
EOF
python3 -I "${CHECK}" compare --targets "${TMP}/targets.json" --name A --reference "${TMP}/ref.csv" --trunc 1e-4 >/dev/null \
  || fail "截断后一致（含 0.7284 边界、截成 0 的 SOL）应当通过"
sed 's/0.25$/0.2601/' "${TMP}/ref.csv" > "${TMP}/ref_bad.csv"
set +e
python3 -I "${CHECK}" compare --targets "${TMP}/targets.json" --name A --reference "${TMP}/ref_bad.csv" --trunc 1e-4 >/dev/null
[[ $? == 1 ]] || fail "权重不一致必须以 1 退出"
python3 -I "${CHECK}" compare --targets "${TMP}/targets.json" --name A --reference "${TMP}/ref.csv" --trunc 1e-4 \
  --until 2026-10-26T18:00:00Z >/dev/null 2>&1
[[ $? == 2 ]] || fail "只有一期、权重没有变化的窗口必须以 2 退出（路径没被检验）"
set -e

cat > "${TMP}/engine.log" <<'EOF'
2026-10-27 02:00:01.000	INFO	trpclog/service_name.go:66	factor_period_start task_id=task-a period_time=2026-10-26T17:55:00Z mode=0	{"service_name": "factor-engine"}
2026-10-27 02:00:05.000	INFO	trpclog/service_name.go:66	factor_period_done task_id=task-a period_time=2026-10-26T17:55:00Z status=complete rows=501 warming=0 duration=4s plan=0s load=200ms compute=3.3s assemble=100ms write=300ms report=50ms	{"service_name": "factor-engine"}
2026-10-27 02:00:02.500	INFO	trpclog/service_name.go:66	factor_period_done task_id=task-1m period_time=2026-10-26T17:59:00Z status=complete rows=523 warming=0 duration=1.5s plan=0s load=300ms compute=700ms assemble=70ms write=400ms report=40ms	{"service_name": "factor-engine"}
2026-10-27 02:01:03.000	INFO	trpclog/service_name.go:66	factor_period_done task_id=task-1m period_time=2026-10-26T18:00:00Z status=degraded rows=523 warming=0 duration=1m2.5s plan=0s load=300ms compute=600ms assemble=70ms write=400ms report=40ms	{"service_name": "factor-engine"}
EOF
cat > "${TMP}/strategy.log" <<'EOF'
2026-10-27 02:00:08.000	INFO	trpclog/service_name.go:66	实例 inst-a 周期 2026-10-26T18:00:00Z ok 耗时：K线至就绪=5s 就绪等待=1.5s 读取=1.2s（View 70ms，当期与上一根行 1.1s） 求值=30ms 提交=2ms 本期处理=1.3s 总计=8s	{"service_name": "strategy"}
EOF
latency="$(python3 -I "${CHECK}" latency --engine-log "${TMP}/engine.log" --strategy-log "${TMP}/strategy.log" \
  --pair A=inst-a:task-a --since 2026-10-26T18:00:00Z --cold 0 --min-steady 1 --table)"
grep -q "稳态 1 期" <<<"${latency}" || fail "latency 应按 bar_end = period_time + 5m 关联：${latency}"
grep -q "收盘→因子开算=1.00 .*因子 compute=3.30 .*因子合计=4.00 .*因子完成→就绪=0.00 .*总计=8.00" <<<"${latency}" \
  || fail "latency 的分段不对：${latency}"
lag="$(python3 -I "${CHECK}" task-lag --engine-log "${TMP}/engine.log" --task task-1m --bar-minutes 1)"
grep -q "边界分钟.*1 期.*收盘→完成 p50 2.50" <<<"${lag}" || fail "收盘 18:00 的 1m 期应归入边界分钟：${lag}"
grep -q "其余分钟 1 期，状态 {'degraded': 1}；收盘→完成 p50 3.00" <<<"${lag}" || fail "task-lag 应保留状态并按收盘计时：${lag}"

echo "strategy check contract passed"
