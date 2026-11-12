#!/usr/bin/env bash
# 校验内嵌的交易日历：每份日历的数据文件 <日历 ID>.json 与它的清单 <日历 ID>.manifest.json 一致。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DATA_DIR="${ROOT}/packages/marketcalendar/data"
python3 - "${DATA_DIR}" <<'PY'
import glob
import hashlib
import json
import os
import sys
from datetime import date

data_dir = sys.argv[1]
manifest_paths = sorted(glob.glob(os.path.join(data_dir, "*.manifest.json")))
if not manifest_paths:
    raise SystemExit("no calendar manifest found")
for manifest_path in manifest_paths:
    with open(manifest_path, encoding="utf-8") as stream:
        manifest = json.load(stream)
    calendar_id = manifest.get("calendar_id")
    if os.path.basename(manifest_path) != f"{calendar_id}.manifest.json":
        raise SystemExit(f"{manifest_path}: calendar_id {calendar_id!r} does not match file name")
    days_path = os.path.join(data_dir, f"{calendar_id}.json")
    with open(days_path, encoding="utf-8") as stream:
        data = json.load(stream)
    if data.get("calendar_id") != calendar_id:
        raise SystemExit(f"{days_path}: calendar_id does not match manifest")

    days = data.get("trading_days")
    if not isinstance(days, list) or not days:
        raise SystemExit(f"{calendar_id}: trading_days must be a non-empty list")
    parsed = [date.fromisoformat(value) for value in days]
    if parsed != sorted(parsed) or len(set(parsed)) != len(parsed):
        raise SystemExit(f"{calendar_id}: dates must be strictly ascending and unique")
    if days[0] != manifest.get("valid_from") or days[-1] != manifest.get("valid_through"):
        raise SystemExit(f"{calendar_id}: manifest coverage does not match calendar data")
    early = data.get("early_closes", {})
    if not isinstance(early, dict) or any(day not in set(days) for day in early):
        raise SystemExit(f"{calendar_id}: early_closes must map trading days to closing times")
    with open(days_path, "rb") as stream:
        digest = "sha256:" + hashlib.sha256(stream.read()).hexdigest()
    if digest != manifest.get("sha256"):
        raise SystemExit(f"{calendar_id}: sha256 mismatch: {digest}")
    print(f"calendar_id={calendar_id} valid_from={days[0]} valid_through={days[-1]} days={len(days)} early_closes={len(early)} sha256={digest}")
PY
