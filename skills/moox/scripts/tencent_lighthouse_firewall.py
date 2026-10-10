#!/usr/bin/env python3
"""Open Tencent Lighthouse firewall ports through moox-cli."""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
from pathlib import Path
from typing import Any
from urllib.parse import parse_qs, urlsplit


DEFAULT_MOOX_PORTS = "20201,20200,20202,11000"
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
        raise ValueError("cannot find Lighthouse instance id in detail URL")

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
        raise ValueError("--detail-url, --instance-id, or --public-ip is required")

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
    parser = argparse.ArgumentParser(description="Open Tencent Lighthouse firewall ports through moox-cli.")
    subcommands = parser.add_subparsers(dest="command", required=True)

    parse_cmd = subcommands.add_parser("parse", help="Parse a Tencent Cloud Lighthouse instance detail URL.")
    parse_cmd.add_argument("--detail-url", required=True, help="Tencent Cloud Lighthouse instance detail URL.")
    parse_cmd.add_argument("--region", default="", help="Override Tencent Cloud region.")
    parse_cmd.set_defaults(func=run_parse)

    add_cmd = subcommands.add_parser("add", help="Add Lighthouse firewall ports through moox-cli.")
    add_cmd.add_argument("--detail-url", default="", help="Tencent Cloud Lighthouse instance detail URL.")
    add_cmd.add_argument("--instance-id", default="", help="Lighthouse instance id.")
    add_cmd.add_argument("--public-ip", default="", help="Resolve Lighthouse instance by public IP.")
    add_cmd.add_argument("--region", default="", help=f"Tencent Cloud region, default {DEFAULT_REGION}.")
    add_cmd.add_argument("--ports", default=DEFAULT_MOOX_PORTS, help="Ports to open.")
    add_cmd.add_argument("--protocol", default="TCP", help="Protocol: TCP, UDP, ICMP, ICMPv6, or ALL.")
    add_cmd.add_argument("--cidr", default="0.0.0.0/0", help="IPv4 CIDR.")
    add_cmd.add_argument("--ipv6-cidr", default="", help="IPv6 CIDR.")
    add_cmd.add_argument("--action", default="ACCEPT", help="Firewall action.")
    add_cmd.add_argument("--description", default="moox services", help="Firewall rule description.")
    add_cmd.add_argument("--endpoint", default="", help="Tencent Cloud Lighthouse API endpoint.")
    add_cmd.add_argument("--firewall-version", type=int, default=0, help="Optional firewall version.")
    add_cmd.add_argument("--moox-cli", default=default_moox_cli(), help="Path to moox-cli.")
    add_cmd.add_argument("--dry-run", action="store_true", help="Pass --dry-run to moox-cli.")
    add_cmd.add_argument("--print-command", action="store_true", help="Print planned argv without executing.")
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
