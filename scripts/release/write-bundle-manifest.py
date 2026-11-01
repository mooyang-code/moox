#!/usr/bin/env python3
"""写产物包清单 moox-bundle.json。

用法：write-bundle-manifest.py <版本> <提交> <单元结果目录> <云函数程序> <输出> <文件列表（\\0 分隔，从标准输入读）>
单元结果目录里是 setup package 输出的 <单元>.json；附件名、摘要、大小都写进清单，目标机按它下载和校验。
"""
import hashlib
import json
import os
import sys


def artifact(path):
    digest = hashlib.sha256()
    with open(path, "rb") as stream:
        for chunk in iter(lambda: stream.read(1 << 20), b""):
            digest.update(chunk)
    return {"file": os.path.basename(path), "sha256": digest.hexdigest(), "size": os.path.getsize(path)}


def main():
    release, commit, results, scf, output = sys.argv[1:6]
    files = [name for name in sys.stdin.buffer.read().decode().split("\0") if name]
    units = {}
    catalogs = set()
    for entry in sorted(os.listdir(results)):
        with open(os.path.join(results, entry), encoding="utf-8") as stream:
            result = json.load(stream)
        unit = artifact(result["archive"])
        unit["manifest"] = result["manifest"]
        units[entry[: -len(".json")]] = unit
        catalogs.add(result["manifest"]["catalog_sha256"])
    if len(catalogs) != 1:
        sys.exit("单元包的服务目录摘要不一致")
    manifest = {
        "version": 1,
        "release": release,
        "commit": commit,
        "catalog_sha256": catalogs.pop(),
        "units": units,
        "collector_scf": artifact(scf),
        "files": files,
    }
    with open(output, "w", encoding="utf-8") as stream:
        json.dump(manifest, stream, ensure_ascii=False, indent=1)


if __name__ == "__main__":
    main()
