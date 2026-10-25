#!/usr/bin/env bash
# import_verify.py 的契约测试：用假的 moox-cli 验证行数一致与不一致两种结果。
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/pack/5m"
printf 'data_time,close\n2026-10-01T00:00:00Z,1\n2026-10-01T00:05:00Z,2\n2026-10-01T00:10:00Z,3\n' > "$tmp/pack/5m/AAA-USDT.csv"
cat > "$tmp/pack/manifest.json" <<JSON
{"space_id":"crypto","dataset_id":"d","freq":"5m","series_tag":"t","files":[{"subject_id":"AAA-USDT","file":"5m/AAA-USDT.csv","rows":3,"first_utc":"2026-10-01T00:00:00Z","last_utc":"2026-10-01T00:10:00Z","sha256":"x"}]}
JSON
cat > "$tmp/preflight.json" <<JSON
{"files":[{"file":"$tmp/pack/5m/AAA-USDT.csv","tail_from":"2026-10-01T00:05:00Z","tail_rows":2}]}
JSON
# 假 moox-cli：按环境变量 FAKE_ROWS 返回对应行数。
cat > "$tmp/moox-cli" <<'SH'
#!/usr/bin/env bash
echo "已导出 ${FAKE_ROWS} 行"
python3 -c "import json,os; print(json.dumps({'rows':[{}]*int(os.environ['FAKE_ROWS'])}))"
SH
chmod +x "$tmp/moox-cli"
FAKE_ROWS=2 python3 -I "$here/import_verify.py" --pack "$tmp/pack" --preflight "$tmp/preflight.json" --moox-cli "$tmp/moox-cli" >/dev/null
if FAKE_ROWS=1 python3 -I "$here/import_verify.py" --pack "$tmp/pack" --preflight "$tmp/preflight.json" --moox-cli "$tmp/moox-cli" >"$tmp/out.txt"; then
  echo "行数不一致时应返回非零" >&2; exit 1
fi
grep -q "行数不一致 AAA-USDT：CSV 2，Storage 1" "$tmp/out.txt"
# 导出被 --page-size 截断时不能当作完整结果：报读取失败。
cat > "$tmp/moox-cli-truncated" <<'SH'
#!/usr/bin/env bash
echo "已导出 2 行，达到 --page-size 上限，区间内还有更多数据"
python3 -c "import json; print(json.dumps({'rows':[{}]*2}))"
SH
chmod +x "$tmp/moox-cli-truncated"
if python3 -I "$here/import_verify.py" --pack "$tmp/pack" --preflight "$tmp/preflight.json" --moox-cli "$tmp/moox-cli-truncated" >"$tmp/out2.txt"; then
  echo "导出被截断时应返回非零" >&2; exit 1
fi
grep -q "读取失败 AAA-USDT" "$tmp/out2.txt"
echo "ok"
