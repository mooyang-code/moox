#!/usr/bin/env python3
"""通过 moox-cli 为腾讯云轻量应用服务器临时开放防火墙端口。

MooX 各主机的入站规则由 `moox-cli setup firewall` 按部署表统一管理；这个脚本只用于手工临时开放某个端口，
端口必须显式指定，不再有默认值。
"""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
from pathlib import Path
from typing import Any
from urllib.parse import parse_qs, urlsplit


DEFAULT_REGION = "ap-guangzhou"

RID_REGION_MAP = {
    "1": "ap-guangzhou",
}


def parse_console_detail_url(url: str, explicit_region: str | None = None) -> dict[str, str]:
    parts = urlsplit(url.strip())
    values = flatten_query(parse_qs(parts.query))

    if parts.fragment:
        fragment = urlsplit(parts.fragment)
        values.update({k: v for k, v in flatten_query(parse_qs(fragment.query)).items() if k not in values})

    nested = values.get("searchParams")
    if nested:
        for key, value in flatten_query(parse_qs(nested)).items():
            values.setdefault(key, value)

    instance_id = values.get("id") or values.get("instanceId") or values.get("instance_id")
    if not instance_id:
        raise ValueError("在详情页 URL 中找不到轻量应用服务器的实例 ID")

    region = explicit_region or values.get("region") or RID_REGION_MAP.get(values.get("rid", ""), DEFAULT_REGION)
    return {
        "instance_id": instance_id.strip(),
        "region": region.strip(),
    }


def flatten_query(values: dict[str, list[str]]) -> dict[str, str]:
    return {key: items[-1] for key, items in values.items() if items}


def default_moox_cli() -> str:
    env_value = os.environ.get("MOOX_CLI")
    if env_value:
        return env_value

    repo_root = Path(__file__).resolve().parents[3]
    bundled = repo_root / "bin" / "moox-cli"
    if bundled.exists():
        return str(bundled)
    return "moox-cli"


def build_add_argv(args: argparse.Namespace) -> dict[str, Any]:
    region = args.region
    instance_id = args.instance_id

    if args.detail_url:
        parsed = parse_console_detail_url(args.detail_url, args.region)
        instance_id = instance_id or parsed["instance_id"]
        region = region or parsed["region"]

    region = region or DEFAULT_REGION
    argv = [
        args.moox_cli,
        "ops",
        "tencent",
        "lighthouse",
        "firewall",
        "add",
        "--region",
        region,
        "--ports",
        args.ports,
        "--protocol",
        args.protocol,
        "--action",
        args.action,
        "--description",
        args.description,
    ]

    if instance_id:
        argv.extend(["--instance-id", instance_id])
    elif args.public_ip:
        argv.extend(["--public-ip", args.public_ip])
    else:
        raise ValueError("必须指定 --detail-url、--instance-id 或 --public-ip")

    if args.cidr:
        argv.extend(["--cidr", args.cidr])
    if args.ipv6_cidr:
        argv.extend(["--ipv6-cidr", args.ipv6_cidr])
    if args.endpoint:
        argv.extend(["--endpoint", args.endpoint])
    if args.firewall_version:
        argv.extend(["--firewall-version", str(args.firewall_version)])
    if args.dry_run:
        argv.append("--dry-run")

    return {
        "argv": argv,
        "instance_id": instance_id or "",
        "public_ip": args.public_ip or "",
        "region": region,
        "ports": args.ports,
    }


def emit_json(value: Any) -> int:
    print(json.dumps(value, ensure_ascii=False, indent=2))
    return 0


def run_parse(args: argparse.Namespace) -> int:
    return emit_json(parse_console_detail_url(args.detail_url, args.region))


def run_add(args: argparse.Namespace) -> int:
    planned = build_add_argv(args)
    if args.print_command:
        planned["will_execute"] = False
        return emit_json(planned)

    result = subprocess.run(
        planned["argv"],
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    if result.stdout:
        sys.stdout.write(result.stdout)
    if result.stderr:
        sys.stderr.write(result.stderr)
    return result.returncode


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="通过 moox-cli 为腾讯云轻量应用服务器临时开放防火墙端口。")
    subcommands = parser.add_subparsers(dest="command", required=True)

    parse_cmd = subcommands.add_parser("parse", help="解析腾讯云轻量应用服务器的实例详情页 URL。")
    parse_cmd.add_argument("--detail-url", required=True, help="轻量应用服务器的实例详情页 URL。")
    parse_cmd.add_argument("--region", default="", help="覆盖地域。")
    parse_cmd.set_defaults(func=run_parse)

    add_cmd = subcommands.add_parser("add", help="开放轻量应用服务器的防火墙端口。")
    add_cmd.add_argument("--detail-url", default="", help="轻量应用服务器的实例详情页 URL。")
    add_cmd.add_argument("--instance-id", default="", help="轻量应用服务器的实例 ID。")
    add_cmd.add_argument("--public-ip", default="", help="按公网 IP 查找实例。")
    add_cmd.add_argument("--region", default="", help=f"地域，默认 {DEFAULT_REGION}。")
    add_cmd.add_argument("--ports", required=True, help="要开放的端口，逗号分隔；没有默认值，必须显式指定。")
    add_cmd.add_argument("--protocol", default="TCP", help="协议：TCP、UDP、ICMP、ICMPv6 或 ALL。")
    add_cmd.add_argument("--cidr", default="0.0.0.0/0", help="IPv4 来源网段；内部端口请收窄到具体地址。")
    add_cmd.add_argument("--ipv6-cidr", default="", help="IPv6 来源网段。")
    add_cmd.add_argument("--action", default="ACCEPT", help="防火墙动作。")
    add_cmd.add_argument("--description", default="moox manual", help="规则描述。")
    add_cmd.add_argument("--endpoint", default="", help="轻量应用服务器 API 的接入点。")
    add_cmd.add_argument("--firewall-version", type=int, default=0, help="可选的防火墙版本。")
    add_cmd.add_argument("--moox-cli", default=default_moox_cli(), help="moox-cli 的路径。")
    add_cmd.add_argument("--dry-run", action="store_true", help="给 moox-cli 传 --dry-run。")
    add_cmd.add_argument("--print-command", action="store_true", help="只打印将要执行的命令，不执行。")
    add_cmd.set_defaults(func=run_add)
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    try:
        return args.func(args)
    except ValueError as exc:
        parser.error(str(exc))
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
