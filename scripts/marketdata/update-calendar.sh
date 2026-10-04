#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SOURCE=""
VALID_THROUGH=""

usage() {
	printf 'usage: %s --source PATH --valid-through YYYY-MM-DD\n' "$0" >&2
}

while (($# > 0)); do
	case "$1" in
	--source)
		(($# >= 2)) || { usage; exit 2; }
		SOURCE="$2"
		shift 2
		;;
	--valid-through)
		(($# >= 2)) || { usage; exit 2; }
		VALID_THROUGH="$2"
		shift 2
		;;
	-h|--help)
		usage
		exit 0
		;;
	*)
		usage
		exit 2
		;;
	esac
done

[[ -n "$SOURCE" && -f "$SOURCE" && -n "$VALID_THROUGH" ]] || { usage; exit 2; }

DATA_DIR="${ROOT}/packages/marketcalendar/data"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

python3 - "$SOURCE" "$VALID_THROUGH" "$TMP_DIR" <<'PY'
import hashlib
import json
import os
import sys
from datetime import date

source_path, valid_through_text, output_dir = sys.argv[1:]
try:
    valid_through = date.fromisoformat(valid_through_text)
except ValueError as exc:
    raise SystemExit(f"invalid --valid-through: {exc}")

with open(source_path, encoding="utf-8") as stream:
    source = json.load(stream)
if not isinstance(source, list) or not source:
    raise SystemExit("calendar source must be a non-empty JSON list")

days = []
for index, value in enumerate(source):
    if not isinstance(value, str):
        raise SystemExit(f"calendar source item {index} is not a string")
    value = value.strip()
    if len(value) == 8 and value.isdigit():
        value = f"{value[:4]}-{value[4:6]}-{value[6:]}"
    try:
        parsed = date.fromisoformat(value)
    except ValueError as exc:
        raise SystemExit(f"calendar source item {index} is invalid: {exc}")
    days.append((parsed, parsed.isoformat()))

parsed_days = [item[0] for item in days]
if parsed_days != sorted(parsed_days) or len(set(parsed_days)) != len(parsed_days):
    raise SystemExit("calendar source dates must be strictly ascending and unique")
if valid_through != parsed_days[-1]:
    raise SystemExit(f"--valid-through must match source last date {parsed_days[-1].isoformat()}")

calendar = {"calendar_id": "cn_stock", "trading_days": [item[1] for item in days]}
data_bytes = (json.dumps(calendar, ensure_ascii=False, indent=2) + "\n").encode("utf-8")
manifest = {
    "calendar_id": "cn_stock",
    "source": "AkShare file_fold/calendar.json",
    "version": 1,
    "data_version": date.today().isoformat(),
    "valid_from": days[0][1],
    "valid_through": days[-1][1],
    "sha256": "sha256:" + hashlib.sha256(data_bytes).hexdigest(),
}

os.makedirs(output_dir, exist_ok=True)
with open(os.path.join(output_dir, "cn_trading_days.json"), "wb") as stream:
    stream.write(data_bytes)
with open(os.path.join(output_dir, "manifest.json"), "w", encoding="utf-8") as stream:
    json.dump(manifest, stream, ensure_ascii=False, indent=2)
    stream.write("\n")
PY

mv "${TMP_DIR}/cn_trading_days.json" "${DATA_DIR}/cn_trading_days.json"
mv "${TMP_DIR}/manifest.json" "${DATA_DIR}/manifest.json"
printf 'updated %s through %s\n' "${DATA_DIR}" "${VALID_THROUGH}"
