#!/usr/bin/env python3
"""初始数据导入的 Storage 行数校验（流程见 references/initial-import.md 第 10 步）。

对导入包里的每个标的，比较 Storage 里 [尾部起点, 文件末根] 内的行数与 CSV 在同一区间内的行数。
尾部起点取自预检（--dry-run --tail-rows）输出里每个文件的 tail_from；预检输出由 --preflight 传入。
只用标准库；每个标的调用一次 moox-cli data rows export，默认 8 个并发。

退出码：0 全部一致；1 有标的行数不一致或读取失败；2 参数或输入有问题。
"""
import argparse
import csv
import json
import os
import subprocess
import sys
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timedelta, timezone


def parse_time(text):
    return datetime.strptime(text, "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=timezone.utc)


def csv_rows_in_window(path, start, end):
    count = 0
    with open(path, newline="", encoding="utf-8") as handle:
        for row in csv.DictReader(handle):
            at = parse_time(row["data_time"])
            if start <= at <= end:
                count += 1
    return count


def storage_rows_in_window(args, manifest, subject, start, end):
    command = [
        args.moox_cli, "data", "rows", "export", "--file", args.moox_toml, "--space", manifest["space_id"],
        "--dataset", manifest["dataset_id"], "--freq", manifest["freq"], "--subject", subject, "--columns", "close",
        "--series-tag", manifest["series_tag"],
        "--start-time", start.strftime("%Y-%m-%dT%H:%M:%SZ"), "--end-time", (end + timedelta(seconds=1)).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "--page-size", str(args.page_size),
    ]
    env = {k: v for k, v in os.environ.items() if k.lower() not in ("http_proxy", "https_proxy")}
    result = subprocess.run(command, capture_output=True, text=True, env=env, timeout=args.timeout)
    if result.returncode != 0:
        raise RuntimeError(result.stderr.strip().splitlines()[-1] if result.stderr.strip() else "moox-cli 失败")
    text = result.stdout
    start_at = text.find("{")
    if start_at < 0:
        raise RuntimeError("moox-cli 没有输出 JSON")
    document = json.loads(text[start_at:])
    return len(document.get("rows", []))


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--pack", required=True, help="导入包目录（含 manifest.json）")
    parser.add_argument("--preflight", required=True, help="预检输出的 JSON 文件（moox-cli import --manifest-pack … --dry-run --tail-rows N 的 stdout）")
    parser.add_argument("--moox-cli", required=True, help="moox-cli 路径（当前版本）")
    parser.add_argument("--moox-toml", default="./moox.toml")
    parser.add_argument("--workers", type=int, default=8)
    parser.add_argument("--page-size", type=int, default=50000, help="每个标的最多导出的行数，要大于尾部窗口行数")
    parser.add_argument("--timeout", type=int, default=300)
    args = parser.parse_args()

    try:
        with open(os.path.join(args.pack, "manifest.json"), encoding="utf-8") as handle:
            manifest = json.load(handle)
        with open(args.preflight, encoding="utf-8") as handle:
            text = handle.read()
        preflight = json.loads(text[text.find("{"):])
    except (OSError, ValueError) as err:
        print(f"读取输入失败：{err}", file=sys.stderr)
        return 2

    tail_from = {}
    for entry in preflight.get("files", []):
        if not entry.get("tail_from"):
            print("预检输出里没有 tail_from：预检要带 --tail-rows 运行", file=sys.stderr)
            return 2
        tail_from[os.path.basename(entry["file"])] = entry["tail_from"]

    jobs = []
    for entry in manifest["files"]:
        name = os.path.basename(entry["file"])
        if name not in tail_from:
            print(f"预检输出里没有 {name}", file=sys.stderr)
            return 2
        jobs.append((entry["subject_id"], os.path.join(args.pack, entry["file"]), parse_time(tail_from[name]), parse_time(entry["last_utc"])))

    def check(job):
        subject, path, start, end = job
        try:
            expected = csv_rows_in_window(path, start, end)
            actual = storage_rows_in_window(args, manifest, subject, start, end)
        except Exception as err:  # noqa: BLE001 - 单个标的失败要报告而不是中断全部
            return subject, None, None, str(err)
        return subject, expected, actual, ""

    bad = 0
    with ThreadPoolExecutor(max_workers=max(1, args.workers)) as pool:
        for subject, expected, actual, error in pool.map(check, jobs):
            if error:
                bad += 1
                print(f"读取失败 {subject}：{error}")
            elif expected != actual:
                bad += 1
                print(f"行数不一致 {subject}：CSV {expected}，Storage {actual}")
    print(f"共核对 {len(jobs)} 个标的，{bad} 个有问题")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
