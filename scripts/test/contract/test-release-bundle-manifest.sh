#!/usr/bin/env bash
# 发布清单契约：用假单元包确认清单列全附件，摘要和大小与文件一致。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/moox-release-contract.XXXXXX")"
trap 'rm -rf "${WORK}"' EXIT

mkdir -p "${WORK}/results"
for unit in host control; do
  printf 'fake-%s' "${unit}" > "${WORK}/moox-unit-${unit}_v9.9.9_linux_amd64.tar.gz"
  python3 -I -c '
import json, sys
json.dump({"archive": sys.argv[1], "manifest": {"profile": sys.argv[2], "catalog_sha256": "abc"}}, open(sys.argv[3], "w"))
' "${WORK}/moox-unit-${unit}_v9.9.9_linux_amd64.tar.gz" "${unit}" "${WORK}/results/${unit}.json"
done
printf 'scf' > "${WORK}/moox-collector-scf_v9.9.9_linux_amd64"
printf 'README.md\0config/a.yaml\0' | python3 -I "${ROOT}/scripts/release/write-bundle-manifest.py" v9.9.9 \
  0123456789012345678901234567890123456789 "${WORK}/results" "${WORK}/moox-collector-scf_v9.9.9_linux_amd64" "${WORK}/moox-bundle.json"
python3 -I -c '
import hashlib, json, sys
m = json.load(open(sys.argv[1]))
assert m["catalog_sha256"] == "abc" and m["files"] == ["README.md", "config/a.yaml"]
assert sorted(m["units"]) == ["control", "host"]
assert m["units"]["host"]["file"] == "moox-unit-host_v9.9.9_linux_amd64.tar.gz"
assert m["units"]["host"]["size"] == len("fake-host")
assert m["units"]["host"]["sha256"] == hashlib.sha256(b"fake-host").hexdigest()
assert m["collector_scf"]["file"] == "moox-collector-scf_v9.9.9_linux_amd64"
' "${WORK}/moox-bundle.json"

# 服务目录不一致时必须拒绝。
python3 -I -c '
import json, sys
r = json.load(open(sys.argv[1])); r["manifest"]["catalog_sha256"] = "other"; json.dump(r, open(sys.argv[1], "w"))
' "${WORK}/results/control.json"
if printf 'README.md\0' | python3 -I "${ROOT}/scripts/release/write-bundle-manifest.py" v9.9.9 x "${WORK}/results" \
  "${WORK}/moox-collector-scf_v9.9.9_linux_amd64" "${WORK}/bad.json" 2>/dev/null; then
  echo "服务目录不一致时应该失败" >&2
  exit 1
fi
echo "release bundle manifest contract passed"
