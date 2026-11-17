#!/usr/bin/env bash
# 发布清单契约：用假组件包确认清单（version 2）列全附件，摘要和大小与文件一致；
# 组件包与 components.json 对不上、缺服务目录摘要时必须失败。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/moox-release-contract.XXXXXX")"
trap 'rm -rf "${WORK}"' EXIT

CATALOG="sha256:$(python3 -I -c 'import hashlib; print(hashlib.sha256(b"catalog").hexdigest())')"
mkdir -p "${WORK}/out"
for component in host-gateway collector; do
  printf 'fake-%s' "${component}" > "${WORK}/out/moox-${component}_v9.9.9_linux_amd64.tar.gz"
done
# 和 moox-cli setup package-components 的输出同一形状。
python3 -I -c '
import hashlib, json, os, sys
out, catalog, target = sys.argv[1:4]
components = {}
for component in ("host-gateway", "collector"):
    name = "moox-%s_v9.9.9_linux_amd64.tar.gz" % component
    raw = open(os.path.join(out, name), "rb").read()
    components[component] = {"file": name, "sha256": hashlib.sha256(raw).hexdigest(), "size": len(raw),
                             "binaries": {"moox-" + component: hashlib.sha256(b"bin-" + component.encode()).hexdigest()}}
json.dump({"catalog_sha256": catalog, "components": components}, open(target, "w"))
' "${WORK}/out" "${CATALOG}" "${WORK}/components.json"
printf 'scf' > "${WORK}/out/moox-collector-scf_v9.9.9_linux_amd64"
write() {
  printf 'README.md\0config/a.yaml\0' | python3 -I "${ROOT}/scripts/release/write-bundle-manifest.py" v9.9.9 \
    0123456789012345678901234567890123456789 "$1" "${WORK}/out" "${WORK}/out/moox-collector-scf_v9.9.9_linux_amd64" "$2"
}
write "${WORK}/components.json" "${WORK}/moox-bundle.json"
python3 -I -c '
import hashlib, json, sys
m = json.load(open(sys.argv[1]))
assert m["version"] == 2 and "units" not in m
assert m["catalog_sha256"] == sys.argv[2] and m["files"] == ["README.md", "config/a.yaml"]
assert sorted(m["components"]) == ["collector", "host-gateway"]
gateway = m["components"]["host-gateway"]
assert gateway["file"] == "moox-host-gateway_v9.9.9_linux_amd64.tar.gz"
assert gateway["size"] == len("fake-host-gateway")
assert gateway["sha256"] == hashlib.sha256(b"fake-host-gateway").hexdigest()
assert gateway["binaries"] == {"moox-host-gateway": hashlib.sha256(b"bin-host-gateway").hexdigest()}
assert m["collector_scf"]["file"] == "moox-collector-scf_v9.9.9_linux_amd64"
' "${WORK}/moox-bundle.json" "${CATALOG}"

# 组件包在 components.json 之后被改动：摘要对不上必须失败。
printf 'tampered' > "${WORK}/out/moox-collector_v9.9.9_linux_amd64.tar.gz"
if write "${WORK}/components.json" "${WORK}/bad.json" 2>/dev/null; then
  echo "组件包和 components.json 不一致时应该失败" >&2
  exit 1
fi
printf 'fake-collector' > "${WORK}/out/moox-collector_v9.9.9_linux_amd64.tar.gz"

# 没有服务目录摘要必须失败。
python3 -I -c '
import json, sys
r = json.load(open(sys.argv[1])); del r["catalog_sha256"]; json.dump(r, open(sys.argv[2], "w"))
' "${WORK}/components.json" "${WORK}/no-catalog.json"
if write "${WORK}/no-catalog.json" "${WORK}/bad.json" 2>/dev/null; then
  echo "缺服务目录摘要时应该失败" >&2
  exit 1
fi
echo "release bundle manifest contract passed"
