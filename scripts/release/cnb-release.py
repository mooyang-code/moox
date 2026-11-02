#!/usr/bin/env python3
"""在 CNB 上发布新版本并撤下旧版本（需要带 repo-release:rw 权限的 CNB_TOKEN 环境变量）。

用法：python3 cnb-release.py <新版本> <产物目录> <发布说明文件> <要删除的旧版本> <仓库目录>
步骤：推送 main 与新 tag → 创建发布 → 逐个上传附件 → 按 SHA256 核对 → 删除旧版本的发布（保留旧 tag）。
"""
import getpass
import hashlib
import json
import os
import re
import subprocess
import sys

REPO = "mooyang-code/moox"


def cnb(*args):
    out = subprocess.run(["cnb", "releases", *args, "--repo", REPO, "-v"], capture_output=True, text=True)
    if out.returncode != 0:
        sys.exit(f"cnb releases {args[0]} 失败：{out.stderr.strip() or out.stdout.strip()}")
    payload = json.loads(out.stdout)
    status = payload.get("status")
    if status and int(status) >= 300:
        sys.exit(f"cnb releases {args[0]} 返回 {status}：{json.dumps(payload.get('data'), ensure_ascii=False)[:500]}")
    return payload.get("data")


def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def asset_matches(asset, path):
    """没有哈希就不算一致：同版本重新构建的文件可能大小相同。"""
    if asset is None or asset.get("size") != os.path.getsize(path) or not asset.get("hash_value"):
        return False
    return asset["hash_value"].lower().split(":")[-1] == sha256(path)


def find_release(tag):
    out = subprocess.run(["cnb", "releases", "get-release-by-tag", "--repo", REPO, "--tag", tag, "-v"], capture_output=True, text=True)
    if out.returncode != 0:
        return None
    payload = json.loads(out.stdout)
    if int(payload.get("status") or 200) >= 300:
        return None
    return payload.get("data")


def main():
    if len(sys.argv) != 6:
        sys.exit(__doc__)
    version, dist, notes, old, repo_dir = sys.argv[1:]
    if not os.environ.get("CNB_TOKEN"):
        if not sys.stdin.isatty():
            sys.exit("先设置 CNB_TOKEN（需要 repo-release:rw 权限），或在交互终端里运行以便输入")
        token = getpass.getpass("请输入 CNB_TOKEN（需要 repo-release:rw 权限，输入不回显）：").strip()
        if not token:
            sys.exit("未输入 CNB_TOKEN")
        # 写入环境变量，后续 cnb 子进程会继承
        os.environ["CNB_TOKEN"] = token
    files = sorted(f for f in os.listdir(dist) if not f.startswith("."))

    print(f"==> 推送 main 与 {version} 到 CNB")
    remote = f"https://cnb:{os.environ['CNB_TOKEN']}@cnb.cool/{REPO}.git"
    # 地址里有令牌，失败时不能让异常把命令打印出来。
    if subprocess.run(["git", "-C", repo_dir, "push", remote, f"refs/remotes/origin/main:refs/heads/main", f"refs/tags/{version}"]).returncode != 0:
        sys.exit("推送到 CNB 失败")

    release = find_release(version)
    if release is None:
        print(f"==> 创建发布 {version}")
        release = cnb("post-release", "--tag-name", version, "--name", f"MooX {version}", "--body-file", notes, "--make-latest", "true")
        release = release if release and release.get("id") else find_release(version)
    rid = str(release["id"])
    print(f"    发布 id {rid}")

    existing = {a["name"]: a for a in release.get("assets") or []}
    for name in files:
        path = os.path.join(dist, name)
        size = os.path.getsize(path)
        if asset_matches(existing.get(name), path):
            print(f"==> 跳过 {name}：CNB 上已有相同文件")
            continue
        print(f"==> 上传 {name}（{size / 1048576:.1f}MB）")
        info = cnb("post-release-asset-upload-url", "--release-id", rid, "--asset-name", name, "--size", str(size), "--overwrite")
        upload_url, verify_url = info["upload_url"], info["verify_url"]
        # 大文件走 HTTP/2 偶尔报 framing 错误，整个发布不能因为一次失败停在半路。地址带签名，失败时不打印。
        uploaded = subprocess.run(["curl", "-sS", "--fail", "--http1.1", "--retry", "5", "--retry-all-errors", "--retry-delay", "5", "-X", "PUT", "-T", path, upload_url, "-o", "/dev/null"])
        if uploaded.returncode != 0:
            sys.exit(f"上传 {name} 失败（curl 退出码 {uploaded.returncode}），重新运行会接着传")
        m = re.search(r"/asset-upload-confirmation/([^/]+)/(.+?)(?:\?|$)", verify_url)
        if not m:
            sys.exit("无法从 verify_url 解析确认参数")
        cnb("post-release-asset-upload-confirmation", "--release-id", rid, "--upload-token", m.group(1), "--asset-path", m.group(2), "--ttl", "0")

    print("==> 核对附件")
    release = find_release(version)
    remote_assets = {a["name"]: a for a in release.get("assets", [])}
    bad = False
    for name in files:
        ok = asset_matches(remote_assets.get(name), os.path.join(dist, name))
        print(f"    {'OK ' if ok else '不一致'} {name}")
        bad |= not ok
    if bad:
        sys.exit("附件核对不一致，未删除旧版本")

    old_release = find_release(old)
    if old_release is None:
        print(f"==> CNB 上没有 {old} 的发布，跳过删除")
    else:
        print(f"==> 删除旧发布 {old}（id {old_release['id']}，保留 git tag）")
        cnb("delete-release", "--release-id", str(old_release["id"]))
    print("完成")


if __name__ == "__main__":
    main()
