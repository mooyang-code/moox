#!/usr/bin/env python3
"""写产物包清单 moox-bundle.json（version 2：每个组件一个包）。

用法：write-bundle-manifest.py <版本> <提交> <components.json> <组件包目录> <云函数程序> <输出> <文件列表（\\0 分隔，从标准输入读）>
components.json 是 moox-cli setup package-components 的输出：{"catalog_sha256": ..., "components": {<组件>: 包}}。
每个包在组件包目录里重新算一遍摘要和大小，和 components.json 不一致就失败；附件名、摘要、大小都写进清单，目标机按它下载和校验。
"""
import hashlib
import json
import os
import re
import sys

DIGEST = re.compile(r"^[0-9a-f]{64}$")
CATALOG = re.compile(r"^sha256:[0-9a-f]{64}$")


def artifact(path):
    digest = hashlib.sha256()
    with open(path, "rb") as stream:
        for chunk in iter(lambda: stream.read(1 << 20), b""):
            digest.update(chunk)
    return {"file": os.path.basename(path), "sha256": digest.hexdigest(), "size": os.path.getsize(path)}


def main():
    release, commit, results, directory, scf, output = sys.argv[1:7]
    files = [name for name in sys.stdin.buffer.read().decode().split("\0") if name]
    with open(results, encoding="utf-8") as stream:
        packaged = json.load(stream)
    catalog = packaged.get("catalog_sha256", "")
    if not CATALOG.match(catalog):
        sys.exit("components.json 没有有效的服务目录摘要 catalog_sha256")
    declared = packaged.get("components") or {}
    if not declared:
        sys.exit("components.json 里没有组件包")
    components = {}
    for component in sorted(declared):
        entry = declared[component]
        name = entry.get("file", "")
        if not name or os.path.basename(name) != name:
            sys.exit("组件 %s 的包名无效：%r" % (component, name))
        actual = artifact(os.path.join(directory, name))
        if actual["sha256"] != entry.get("sha256") or actual["size"] != entry.get("size"):
            sys.exit("组件 %s 的包 %s 和 components.json 记的摘要或大小不一致" % (component, name))
        binaries = entry.get("binaries") or {}
        if not binaries or not all(DIGEST.match(value) for value in binaries.values()):
            sys.exit("组件 %s 没有列出二进制或摘要无效" % component)
        actual["binaries"] = dict(sorted(binaries.items()))
        components[component] = actual
    manifest = {
        "version": 2,
        "release": release,
        "commit": commit,
        "catalog_sha256": catalog,
        "components": components,
        "collector_scf": artifact(scf),
        "files": files,
    }
    with open(output, "w", encoding="utf-8") as stream:
        json.dump(manifest, stream, ensure_ascii=False, indent=1)


if __name__ == "__main__":
    main()
