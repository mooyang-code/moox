#!/usr/bin/env bash
# 因子转换工具链的契约测试（只依赖 Go 与 Python 标准库，不需要 pandas）：
# factor-expr.sh check / eval 能处理生产因子库，factor_py_check.py 的 synthetic、rows-to-csv、compare 按约定工作，
# compare 对一致的结果判通过、对写错的转写判失败。
set -euo pipefail

SCRIPT_DIR="$(cd "${BASH_SOURCE[0]%/*}" && pwd -P)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd -P)"
FX="${SCRIPT_DIR}/factor-expr.sh"
CHECK="${SCRIPT_DIR}/factor_py_check.py"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/moox-factor-convert.XXXXXX")"
trap 'rm -rf "${TMP}"' EXIT
fail() { echo "factor convert contract: $*" >&2; exit 1; }

"${FX}" check --library "${REPO_ROOT}/modules/factor/factors/xbx.yaml" > "${TMP}/check.jsonl"
[[ "$(grep -c '"ok":true' "${TMP}/check.jsonl")" == 11 ]] || fail "生产因子库应当 11 个全部编译通过"
grep -q '"factor_id":"zscore_abs_mean_q".*"lookback":358' "${TMP}/check.jsonl" || fail "zscore_abs_mean_q 的回看应按参数上限推导为 358"
"${FX}" functions --json | python3 -c 'import json,sys; names={f["name"] for f in json.load(sys.stdin)}; assert {"ts_mean","cs_rank","where","ema","ema_relaxed","ts_mean_relaxed"} <= names'

# factor-dsl.md 第 4 节 rolling().apply(内置函数) 对照表里的写法都要能编译（逐值等价性已在文档里记录过核对结果）
cat > "${TMP}/apply.yaml" <<'EOF'
- factor_id: apply_map
  name: apply 对照表
  spec: |
    params: {n: {type: int, min: 4, max: 100, default: 20}}
    outputs:
      np_std: ts_std(close, n) * sqrt((n - 1) / n)
      np_var: ts_var(close, n) * (n - 1) / n
      np_argmax: n - 1 - ts_argmax(close, n)
      np_ptp: ts_max(close, n) - ts_min(close, n)
      np_pct: ts_quantile(close, n, 0.3)
      sp_skew: ts_skew(close, n) * (n - 2) / sqrt(n * (n - 1))
      sp_kurt: (ts_kurt(close, n) * (n - 2) * (n - 3) / (n - 1) - 6) / (n + 1)
EOF
"${FX}" check --library "${TMP}/apply.yaml" | grep -q '"ok":true' || fail "apply 对照表里的写法应当全部编译通过"

python3 "${CHECK}" synthetic --subjects 3 --bars 120 --output "${TMP}/bars.csv"
[[ "$(wc -l < "${TMP}/bars.csv" | tr -d ' ')" == 361 ]] || fail "合成数据应为 3×120 行加表头"

cat > "${TMP}/lib.yaml" <<'EOF'
- factor_id: bias
  name: Bias
  spec: |
    params: {window: {type: int, min: 2, max: 120, default: 20}}
    output: close / ts_mean(close, window)
- factor_id: bias_wrong
  name: 多减了 1 的 Bias
  spec: |
    params: {window: {type: int, min: 2, max: 120, default: 20}}
    output: close / ts_mean(close, window) - 1
EOF
"${FX}" eval --library "${TMP}/lib.yaml" --factor-id bias --input "${TMP}/bars.csv" --freq 1m --output "${TMP}/a.csv"
"${FX}" eval --library "${REPO_ROOT}/modules/factor/factors/xbx.yaml" --factor-id bias --input "${TMP}/bars.csv" --freq 1m --output "${TMP}/b.csv"
"${FX}" eval --library "${TMP}/lib.yaml" --factor-id bias_wrong --input "${TMP}/bars.csv" --freq 1m --output "${TMP}/wrong.csv"
python3 "${CHECK}" compare --expected "${TMP}/b.csv" --actual "${TMP}/a.csv" --freq 1m --lookback 20 >/dev/null || fail "相同的写法应当通过"
if python3 "${CHECK}" compare --expected "${TMP}/b.csv" --actual "${TMP}/wrong.csv" --freq 1m --lookback 20 >/dev/null; then
  fail "写错的转写必须判失败"
fi

cat > "${TMP}/rows.json" <<'EOF'
{"rows": [
 {"key": {"subject_id": "BTC-USDT", "data_time": "2026-10-18T00:01:00Z", "series_tag": "t"}, "fields": [{"field_id": "close", "value": {"double_value": 101.5}}, {"field_id": "volume", "value": {"int_value": "12"}}]},
 {"key": {"subject_id": "BTC-USDT", "data_time": "2026-10-18T00:00:00Z", "series_tag": "t"}, "fields": [{"field_id": "close", "value": {"double_value": 100}}, {"field_id": "volume", "value": {}}]}
]}
EOF
python3 "${CHECK}" rows-to-csv "${TMP}/rows.json" --output "${TMP}/rows.csv" 2>/dev/null
expected=$'data_time,subject_id,series_tag,close,volume\n2026-10-18T00:00:00Z,BTC-USDT,t,100.0,\n2026-10-18T00:01:00Z,BTC-USDT,t,101.5,12.0'
[[ "$(tr -d '\r' < "${TMP}/rows.csv")" == "${expected}" ]] || fail "rows-to-csv 输出不符合约定"

echo "factor convert contract passed"
