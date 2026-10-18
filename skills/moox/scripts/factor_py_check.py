#!/usr/bin/env python3
"""把旧 Python 因子转写成表达式（DSL）之后的核对工具，流程见 references/factor-dsl.md。

子命令：
  synthetic    生成合成 OHLCV K 线 CSV（只用标准库）
  rows-to-csv  把 data-kline.sh / moox-cli data rows export 输出的 JSON 转成 CSV
  run-python   按旧契约 compute(df, params, context) 运行原 Python 因子，输出结果 CSV（需要 pandas）
  compare      逐值比较原 Python 结果与 factorexpr eval 的结果，按验收规则给出通过 / 失败（只用标准库）

CSV 统一格式：data_time（RFC3339 UTC）、subject_id、series_tag、各数值列；空值写空串。
"""
import argparse
import csv
import importlib.util
import json
import math
import random
import sys
from collections import defaultdict
from datetime import datetime, timedelta, timezone

IDENTITY = ("data_time", "subject_id", "series_tag")
# compare 的分类：一致、双空、暖机期新空（允许）、窗口覆盖空洞的新空（允许，引擎整因子屏蔽）、其余新空、原空新有值、离散跳变、超差。
BUCKETS = ("compared", "match", "both_null", "old_value_new_null_warmup", "old_value_new_null_gap", "old_value_new_null",
           "old_null_new_value", "discrete_flip", "mismatch")
# 与因子模块支持的频率一致。
FREQUENCIES = {"1m": 60, "5m": 300, "15m": 900, "30m": 1800, "1h": 3600, "4h": 14400, "1d": 86400}


def parse_time(raw):
    raw = raw.strip()
    if raw.endswith("Z"):
        raw = raw[:-1] + "+00:00"
    value = datetime.fromisoformat(raw)
    if value.tzinfo is None:
        value = value.replace(tzinfo=timezone.utc)
    return value.astimezone(timezone.utc)


def format_time(value):
    return value.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def parse_freq(text):
    if text not in FREQUENCIES:
        raise SystemExit(f"频率只支持 {', '.join(FREQUENCIES)}，得到 {text!r}")
    return timedelta(seconds=FREQUENCIES[text])


def number(raw):
    if raw is None:
        return math.nan
    raw = str(raw).strip()
    if raw == "" or raw.lower() in ("null", "nan", "none"):
        return math.nan
    return float(raw)


def write_csv(path, header, rows):
    handle = open(path, "w", newline="") if path else sys.stdout
    try:
        writer = csv.writer(handle)
        writer.writerow(header)
        writer.writerows(rows)
    finally:
        if path:
            handle.close()


def cell(value):
    if value is None:
        return ""
    if isinstance(value, float) and (math.isnan(value) or math.isinf(value)):
        return ""
    return repr(float(value)) if isinstance(value, float) else str(value)


# ---------------------------------------------------------------- synthetic


def cmd_synthetic(args):
    rng = random.Random(args.seed)
    step = parse_freq(args.freq)
    start = parse_time(args.start)
    rows = []
    for s in range(args.subjects):
        subject = f"S{s:03d}-USDT"
        price = 100 * (1 + s)
        dropped = set(rng.sample(range(args.bars // 2, args.bars), args.gaps)) if args.gaps else set()
        for i in range(args.bars):
            open_ = price
            price *= math.exp(rng.gauss(0, 0.004))
            if i % 97 == 13:
                price = open_  # 偶尔收平，覆盖“涨跌为 0”和并列名次
            high = max(open_, price) * (1 + abs(rng.gauss(0, 0.002)))
            low = min(open_, price) * (1 - abs(rng.gauss(0, 0.002)))
            volume = rng.uniform(100, 1000)
            if i in dropped:
                continue
            rows.append([format_time(start + i * step), subject, args.tag,
                         repr(open_), repr(high), repr(low), repr(price), repr(volume), repr(volume * price)])
    write_csv(args.output, list(IDENTITY) + ["open", "high", "low", "close", "volume", "quote_volume"], rows)


# ---------------------------------------------------------------- rows-to-csv


def typed(value):
    if not isinstance(value, dict):
        return value
    for key in ("double_value", "int_value", "string_value", "bool_value"):
        if key in value:
            return value[key]
    return None


def cmd_rows_to_csv(args):
    records = {}
    columns = []
    for path in args.inputs:
        with open(path) as handle:
            payload = json.load(handle)
        for row in payload.get("rows", []):
            key = row.get("key", {})
            identity = (format_time(parse_time(key["data_time"])), key.get("subject_id", ""), key.get("series_tag", ""))
            values = records.setdefault(identity, {})
            for field in row.get("fields", []):
                name = field["field_id"]
                if args.columns and name not in args.columns:
                    continue
                if name not in columns:
                    columns.append(name)
                values[name] = typed(field.get("value"))
    if args.columns:
        columns = [c for c in args.columns if c in columns]
    ordered = sorted(records, key=lambda k: (k[1], k[2], k[0]))
    write_csv(args.output, list(IDENTITY) + columns,
              [list(k) + [cell(number(records[k].get(c))) for c in columns] for k in ordered])
    print(f"{len(ordered)} 行，{len({(k[1], k[2]) for k in ordered})} 条序列，列：{', '.join(columns)}", file=sys.stderr)


# ---------------------------------------------------------------- run-python


def cmd_run_python(args):
    import pandas as pd

    spec = importlib.util.spec_from_file_location("legacy_factor", args.factor_file)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    params = json.loads(args.params)
    bars = pd.read_csv(args.input, dtype={"subject_id": str, "series_tag": str}, keep_default_na=False, na_values=[""])
    if "subject_id" not in bars:
        bars["subject_id"] = "S"
    if "series_tag" not in bars:
        bars["series_tag"] = ""
    bars["series_tag"] = bars["series_tag"].fillna("")
    bars["data_time"] = pd.to_datetime(bars["data_time"], utc=True)
    inputs = [c.strip() for c in args.inputs.split(",")] if args.inputs else \
        [c for c in bars.columns if c not in IDENTITY]
    missing = [c for c in inputs if c not in bars.columns]
    if missing:
        raise SystemExit(f"输入 CSV 缺少列：{missing}")
    times = sorted(bars["data_time"].unique())
    # 旧 worker 的 context：period_time 是目标周期（这里取最后一根）的 Unix 秒，period_times 是本次输入覆盖的周期，
    # 时序因子另有 subject_id，截面因子另有 expected_subjects 与 available_subjects。
    base_context = {
        "period_time": int(pd.Timestamp(times[-1]).timestamp()),
        "frequency": args.freq,
        "period_times": [format_time(pd.Timestamp(t).to_pydatetime()) for t in times],
    }
    frames = []
    if args.type == "timeseries":
        # 旧契约：逐标的调用，输入只有 data_time、series_tag 与声明的输入列，按 (data_time, series_tag) 升序。
        for subject, group in bars.groupby("subject_id", sort=True):
            df = group.sort_values(["data_time", "series_tag"])[["data_time", "series_tag", *inputs]].reset_index(drop=True)
            out = module.compute(df, params, {**base_context, "subject_id": subject})
            out = out.copy()
            out["subject_id"] = subject
            frames.append(out)
    else:
        # 旧契约：截面因子一次拿到全部标的，多一列 subject_id，按 (data_time, series_tag, subject_id) 升序。
        df = bars.sort_values(["data_time", "series_tag", "subject_id"])[["data_time", "series_tag", "subject_id", *inputs]]
        subjects = sorted(df["subject_id"].unique())
        context = {**base_context, "expected_subjects": subjects, "available_subjects": subjects}
        frames.append(module.compute(df.reset_index(drop=True), params, context).copy())
    result = pd.concat(frames, ignore_index=True)
    outputs = [c for c in result.columns if c not in IDENTITY]
    result = result.sort_values(["subject_id", "series_tag", "data_time"])
    rows = []
    for record in result[["data_time", "subject_id", "series_tag", *outputs]].itertuples(index=False):
        rows.append([format_time(record[0].to_pydatetime()), record[1], record[2]] + [cell(float(v)) for v in record[3:]])
    write_csv(args.output, list(IDENTITY) + outputs, rows)
    print(f"{len(rows)} 行，输出列：{', '.join(outputs)}", file=sys.stderr)


# ---------------------------------------------------------------- compare


def read_result(path):
    with open(path, newline="") as handle:
        reader = csv.DictReader(handle)
        columns = [c for c in reader.fieldnames if c not in IDENTITY]
        rows = {}
        for line, record in enumerate(reader, start=2):
            key = (format_time(parse_time(record["data_time"])), record.get("subject_id") or "S", record.get("series_tag") or "")
            if key in rows:
                raise SystemExit(f"{path} 第 {line} 行与前面的行身份重复：{key}")
            rows[key] = {c: number(record[c]) for c in columns}
    return columns, rows


def in_range(key, start, end):
    at = parse_time(key[0])
    return (start is None or at >= start) and (end is None or at < end)


def cmd_compare(args):
    exp_cols, expected = read_result(args.expected)
    act_cols, actual = read_result(args.actual)
    step = parse_freq(args.freq)
    lookback, output_lookbacks = args.lookback, {}
    if args.check:
        with open(args.check) as handle:
            for line in handle:
                item = json.loads(line)
                if item.get("factor_id") == args.factor_id or (not args.factor_id and item.get("ok")):
                    lookback, output_lookbacks = item["lookback"], item.get("output_lookbacks", {})
                    break
            else:
                raise SystemExit(f"{args.check} 里没有 {args.factor_id} 的编译结果")
    if lookback is None:
        raise SystemExit("需要 --check（factor-expr.sh check 的输出）或 --lookback")
    start = parse_time(args.start) if args.start else None
    end = parse_time(args.end) if args.end else None
    rename = dict(item.split("=", 1) for item in args.rename)
    tolerances = {k: float(v) for k, v in (item.split("=", 1) for item in args.column_rtol)}
    pairs = []
    for col in exp_cols:
        target = rename.get(col, col)
        if target in act_cols:
            pairs.append((col, target))
    missing_cols = [c for c in exp_cols if rename.get(c, c) not in act_cols]
    extra_cols = [c for c in act_cols if c not in {t for _, t in pairs}]

    # 原结果覆盖输入的每一行：据此得到每条序列的第一根 K 线与存在的时刻。
    # 暖机期：距第一根不足“该输出的回看 − 1”个周期（严格窗口不满）；
    # 窗口有空洞：按因子的回看，从第一根起算的窗口里缺了 K 线（引擎会屏蔽整个因子的输出）。
    present = defaultdict(set)
    for key in expected:
        present[(key[1], key[2])].add(parse_time(key[0]))
    first = {series: min(times) for series, times in present.items()}

    def warm(key, window):
        return parse_time(key[0]) - first[(key[1], key[2])] < (window - 1) * step

    def gapped(key):
        at, series = parse_time(key[0]), (key[1], key[2])
        lo = max(at - (lookback - 1) * step, first[series])
        while lo <= at:
            if lo not in present[series]:
                return True
            lo += step
        return False

    exp_keys = {k for k in expected if in_range(k, start, end)}
    act_keys = {k for k in actual if in_range(k, start, end)}
    report = {"lookback": lookback, "output_lookbacks": output_lookbacks,
              "missing_columns": missing_cols, "extra_columns": extra_cols,
              "rows_only_in_expected": len(exp_keys - act_keys),
              "rows_only_in_actual": len(act_keys - exp_keys), "columns": {}}
    failures = []
    if missing_cols:
        failures.append(f"DSL 结果缺少输出列 {missing_cols}（输出列名必须与原因子一致）")
    if report["rows_only_in_expected"] or report["rows_only_in_actual"]:
        failures.append(f"行集合不一致：只在原结果 {report['rows_only_in_expected']} 行，只在 DSL 结果 {report['rows_only_in_actual']} 行")
    common = sorted(exp_keys & act_keys, key=lambda k: (k[1], k[2], k[0]))
    for old_col, new_col in pairs:
        rtol = tolerances.get(new_col, args.rtol)
        window = output_lookbacks.get(new_col, lookback)
        stats = dict.fromkeys(BUCKETS, 0)
        examples = defaultdict(list)
        worst = 0.0
        for key in common:
            a, b = expected[key][old_col], actual[key][new_col]
            stats["compared"] += 1
            a_nan, b_nan = math.isnan(a), math.isnan(b)
            if a_nan and b_nan:
                stats["both_null"] += 1
                continue
            if b_nan:
                if warm(key, window):
                    bucket = "old_value_new_null_warmup"
                elif gapped(key):
                    bucket = "old_value_new_null_gap"
                else:
                    bucket = "old_value_new_null"
            elif a_nan:
                bucket = "old_null_new_value"
            else:
                diff = abs(a - b)
                if diff <= args.atol + rtol * max(abs(a), abs(b)):
                    stats["match"] += 1
                    worst = max(worst, diff / max(1.0, abs(a)))
                    continue
                bucket = "discrete_flip" if new_col in args.discrete else "mismatch"
            stats[bucket] += 1
            if len(examples[bucket]) < args.examples:
                examples[bucket].append({"data_time": key[0], "subject_id": key[1], "series_tag": key[2], "python": a, "dsl": b})
        both_value = stats["match"] + stats["mismatch"] + stats["discrete_flip"]
        column = {"rtol": rtol, "lookback": window, "max_rel_error": worst, **stats, "examples": examples}
        report["columns"][new_col] = column
        if stats["mismatch"]:
            failures.append(f"{new_col}：{stats['mismatch']} 个值超出容差 {rtol:g}")
        if stats["discrete_flip"] > args.max_discrete_ratio * max(both_value, 1):
            failures.append(f"{new_col}：离散跳变 {stats['discrete_flip']} 个，超过 {args.max_discrete_ratio:.0%}")
        if stats["old_null_new_value"]:
            failures.append(f"{new_col}：{stats['old_null_new_value']} 个原来为空、DSL 却有值")
        if stats["old_value_new_null"] and not args.allow_new_null:
            failures.append(f"{new_col}：有 {stats['old_value_new_null']} 个原来有值、DSL 为空，既不在暖机期也不在空洞窗口（需逐条解释后用 --allow-new-null 放行）")
        if stats["match"] == 0:
            failures.append(f"{new_col}：没有一个双方都有值且一致的点，样本不足或写法不对")
    report["failures"] = failures
    report["passed"] = not failures
    if args.report:
        with open(args.report, "w") as handle:
            json.dump(report, handle, ensure_ascii=False, indent=2)
    print(f"{'列':<28}{'比较':>8}{'一致':>8}{'双空':>8}{'暖机新空':>9}{'空洞新空':>9}{'其余新空':>9}{'旧空新值':>9}{'跳变':>6}{'超差':>6}  最大相对误差")
    for name, c in report["columns"].items():
        print(f"{name:<28}{c['compared']:>8}{c['match']:>8}{c['both_null']:>8}{c['old_value_new_null_warmup']:>9}"
              f"{c['old_value_new_null_gap']:>9}{c['old_value_new_null']:>9}{c['old_null_new_value']:>9}"
              f"{c['discrete_flip']:>6}{c['mismatch']:>6}  {c['max_rel_error']:.3g}")
        for bucket in ("mismatch", "discrete_flip", "old_value_new_null", "old_null_new_value"):
            for example in c["examples"].get(bucket, []):
                print(f"    {bucket}: {example}")
    if extra_cols:
        print(f"DSL 多出的列（不参与比较）：{extra_cols}")
    if failures:
        print("\n未通过：")
        for failure in failures:
            print(f"  - {failure}")
        return 1
    print("\n通过")
    return 0


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = parser.add_subparsers(dest="command", required=True)

    p = sub.add_parser("synthetic", help="生成合成 OHLCV K 线")
    p.add_argument("--subjects", type=int, default=5)
    p.add_argument("--bars", type=int, default=600)
    p.add_argument("--freq", default="1m")
    p.add_argument("--start", default="2026-10-01T00:00:00Z")
    p.add_argument("--tag", default="venue:binance|market:spot|source:spot_http")
    p.add_argument("--gaps", type=int, default=0, help="每个标的在后半段随机删掉的行数，用来观察空洞语义")
    p.add_argument("--seed", type=int, default=20261018)
    p.add_argument("--output")
    p.set_defaults(func=cmd_synthetic)

    p = sub.add_parser("rows-to-csv", help="把时序行 JSON 转成 CSV")
    p.add_argument("inputs", nargs="+")
    p.add_argument("--columns", nargs="*", help="只保留这些列")
    p.add_argument("--output")
    p.set_defaults(func=cmd_rows_to_csv)

    p = sub.add_parser("run-python", help="按旧契约运行原 Python 因子")
    p.add_argument("--factor-file", required=True)
    p.add_argument("--params", default="{}", help="原目录里的 params，JSON")
    p.add_argument("--type", choices=["timeseries", "cross_section"], default="timeseries")
    p.add_argument("--freq", required=True, choices=list(FREQUENCIES), help="源数据频率，传给旧接口的 context.frequency")
    p.add_argument("--inputs", help="原目录里的 input_columns，逗号分隔；不写时传入全部数值列")
    p.add_argument("--input", required=True)
    p.add_argument("--output")
    p.set_defaults(func=cmd_run_python)

    p = sub.add_parser("compare", help="逐值比较原结果与 DSL 结果")
    p.add_argument("--expected", required=True, help="run-python 的输出")
    p.add_argument("--actual", required=True, help="factorexpr eval 的输出")
    p.add_argument("--freq", required=True, choices=list(FREQUENCIES), help="源数据频率")
    p.add_argument("--check", help="factor-expr.sh check 的输出（JSON Lines），取因子回看与每个输出的回看")
    p.add_argument("--factor-id", help="--check 文件里有多个因子时指定")
    p.add_argument("--lookback", type=int, help="没有 --check 时手填因子回看（所有输出共用）")
    p.add_argument("--start", help="只比较 data_time ≥ start 的行（RFC3339）；提前加载的预热数据用它排除")
    p.add_argument("--end", help="只比较 data_time < end 的行（RFC3339）")
    p.add_argument("--rtol", type=float, default=1e-9)
    p.add_argument("--atol", type=float, default=1e-12)
    p.add_argument("--column-rtol", nargs="*", default=[], help="按列放宽容差，例如 zf_std_20=1e-7")
    p.add_argument("--discrete", nargs="*", default=[], help="排名类输出列：超差按离散跳变计")
    p.add_argument("--max-discrete-ratio", type=float, default=0.01)
    p.add_argument("--allow-new-null", action="store_true", help="暖机期与空洞窗口之外的“原有值、DSL 为空”已逐条解释")
    p.add_argument("--rename", nargs="*", default=[], help="原列名=DSL 列名（只在确需改名时用）")
    p.add_argument("--examples", type=int, default=3)
    p.add_argument("--report", help="把完整报告写成 JSON")
    p.set_defaults(func=cmd_compare)

    args = parser.parse_args()
    sys.exit(args.func(args) or 0)


if __name__ == "__main__":
    main()
