#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DATA_DIR="${ROOT}/packages/marketcalendar/data"
python3 - "${DATA_DIR}/cn_trading_days.json" "${DATA_DIR}/manifest.json" <<'PY'
import hashlib
import json
import sys
from datetime import date

days_path, manifest_path = sys.argv[1:]
with open(days_path, encoding="utf-8") as stream:
    data = json.load(stream)
with open(manifest_path, encoding="utf-8") as stream:
    manifest = json.load(stream)

days = data.get("trading_days")
if not isinstance(days, list) or not days:
    raise SystemExit("calendar trading_days must be a non-empty list")
parsed = [date.fromisoformat(value) for value in days]
if parsed != sorted(parsed) or len(set(parsed)) != len(parsed):
    raise SystemExit("calendar dates must be strictly ascending and unique")
if days[0] != manifest.get("valid_from") or days[-1] != manifest.get("valid_through"):
    raise SystemExit("manifest coverage does not match calendar data")
with open(days_path, "rb") as stream:
    digest = "sha256:" + hashlib.sha256(stream.read()).hexdigest()
if digest != manifest.get("sha256"):
    raise SystemExit(f"calendar sha256 mismatch: {digest}")
print(f"calendar_id={manifest.get('calendar_id')} valid_from={days[0]} valid_through={days[-1]} days={len(days)} sha256={digest}")
PY
