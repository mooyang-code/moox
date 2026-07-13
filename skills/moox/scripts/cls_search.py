#!/usr/bin/env python3
"""CLS (Cloud Log Service) 日志查询工具

通过腾讯云 CLS SearchLog API 查询日志。
支持 CQL 查询语法、时间范围、自动翻页、多种输出格式。

配置优先级（从高到低）：
  1. 命令行参数（--secret-id / --secret-key）
  2. 环境变量 CLS_SECRET_ID / CLS_SECRET_KEY
  3. .env 文件（当前目录或 skill 根目录）

API 文档：https://cloud.tencent.com/document/product/614/56447
"""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
import time
from datetime import datetime, timedelta
from typing import Dict, List, Optional, Tuple

# ── Optional deps ────────────────────────────────────────────
try:
    from dotenv import load_dotenv as _dotenv_load
except ImportError:
    _dotenv_load = None

try:
    from tencentcloud.cls.v20201016 import cls_client, models
    from tencentcloud.common import credential
    from tencentcloud.common.exception.tencent_cloud_sdk_exception import (
        TencentCloudSDKException,
    )
    from tencentcloud.common.profile.client_profile import ClientProfile
    from tencentcloud.common.profile.http_profile import HttpProfile
except ImportError:
    print(
        "ERROR: tencentcloud-sdk-python-cls not installed.\n"
        "Run: pip3 install tencentcloud-sdk-python-cls",
        file=sys.stderr,
    )
    sys.exit(1)


# ── ANSI colors (auto-disable when not TTY) ─────────────────
class C:
    """ANSI color codes. Auto-disabled when stdout is not a TTY (piped output)."""
    _enabled = sys.stdout.isatty()

    RED = "\033[0;31m" if _enabled else ""
    GREEN = "\033[0;32m" if _enabled else ""
    YELLOW = "\033[1;33m" if _enabled else ""
    CYAN = "\033[0;36m" if _enabled else ""
    DIM = "\033[2m" if _enabled else ""
    BOLD = "\033[1m" if _enabled else ""
    NC = "\033[0m" if _enabled else ""


def color_level(level: str) -> str:
    """Color-code log level."""
    level_upper = level.upper() if level else ""
    colors = {
        "ERROR": C.RED,
        "WARN": C.YELLOW,
        "WARNING": C.YELLOW,
        "INFO": C.GREEN,
        "DEBUG": C.DIM,
    }
    color = colors.get(level_upper, "")
    return f"{color}{level_upper:>5}{C.NC}" if color else f"{level_upper:>5}"


# ── .env loading ─────────────────────────────────────────────
def load_dotenv():
    """Try loading .env files: cwd -> skill root dir. Silent on failure."""
    if _dotenv_load is None:
        return
    _dotenv_load()  # current working directory
    skill_dir = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    env_file = os.path.join(skill_dir, ".env")
    if os.path.exists(env_file):
        _dotenv_load(env_file)


# ── Config resolution ────────────────────────────────────────
def get_config(cli_value: Optional[str], env_key: str, default: Optional[str] = None) -> Optional[str]:
    """Resolve config: CLI arg > env var > default."""
    if cli_value is not None:
        return cli_value
    env_value = os.environ.get(env_key, "")
    if env_value:
        return env_value
    return default


def load_credentials(
    cli_secret_id: Optional[str],
    cli_secret_key: Optional[str],
) -> Tuple[str, str]:
    """Load SecretID/SecretKey with priority: CLI > env > .env."""
    # 1. CLI args
    if cli_secret_id and cli_secret_key:
        return cli_secret_id, cli_secret_key

    # 2. Env vars (including .env loaded earlier)
    sid = os.environ.get("CLS_SECRET_ID", "")
    skey = os.environ.get("CLS_SECRET_KEY", "")
    if sid and skey:
        return sid, skey

    print(
        "ERROR: No credentials found. Provide via:\n"
        "  --secret-id/--secret-key, CLS_SECRET_ID/CLS_SECRET_KEY env vars, or .env file",
        file=sys.stderr,
    )
    sys.exit(1)


# ── Time parsing ─────────────────────────────────────────────
def parse_time(value: Optional[str], default_ms: int) -> int:
    """Parse time string to millisecond timestamp.

    Accepts:
      - millisecond timestamp (13 digits)
      - second timestamp (10 digits)
      - datetime string: "2026-03-17 10:00:00"
      - relative: "-1h", "-30m", "-7d", "-3600s"
      - natural: "1h" (same as "-1h", without leading dash)
    """
    if value is None:
        return default_ms

    value = value.strip()

    # Relative time: "-1h", "-30m", "-7d", "-3600s"
    # Also accept without dash: "1h", "30m" (treat as "ago")
    rel_match = re.match(r'^-?(\d+)([hmds])$', value)
    if rel_match:
        num = int(rel_match.group(1))
        unit = rel_match.group(2)
        delta_map = {"s": 1, "m": 60, "h": 3600, "d": 86400}
        return int((time.time() - num * delta_map[unit]) * 1000)

    # Pure digits — timestamp
    if value.isdigit():
        ts = int(value)
        if ts < 1e12:  # seconds
            return ts * 1000
        return ts  # already ms

    # Datetime string
    for fmt in ("%Y-%m-%d %H:%M:%S", "%Y-%m-%d", "%Y-%m-%dT%H:%M:%S"):
        try:
            dt = datetime.strptime(value, fmt)
            return int(dt.timestamp() * 1000)
        except ValueError:
            continue

    print(f"ERROR: Cannot parse time '{value}'", file=sys.stderr)
    sys.exit(1)


# ── Value truncation ─────────────────────────────────────────
def truncate(value: str, max_len: int = 200) -> str:
    """Truncate long values for pretty display."""
    if len(value) <= max_len:
        return value
    return value[:max_len] + f"{C.DIM}...({len(value)} chars){C.NC}"


# ── Output formatting ────────────────────────────────────────
def format_pretty(
    results: List[Dict],
    analysis_records: Optional[List] = None,
    fields_filter: Optional[List[str]] = None,
):
    """Human-readable one-line-per-log format."""
    if not results:
        print(f"{C.YELLOW}No logs found.{C.NC}", file=sys.stderr)
        return

    for log_item in results:
        if isinstance(log_item, dict):
            fields = log_item
        else:
            print(json.dumps(log_item, ensure_ascii=False))
            continue

        # If fields filter is set, show only those fields as compact JSON
        if fields_filter:
            filtered = {k: fields.get(k, "") for k in fields_filter if fields.get(k, "") != ""}
            print(json.dumps(filtered, ensure_ascii=False))
            continue

        ts = fields.get("__TIMESTAMP__", fields.get("timestamp", fields.get("time", "")))
        # Try to format timestamp
        if ts and str(ts).isdigit():
            ts_val = int(ts)
            if ts_val > 1e12:
                ts_val = ts_val / 1000
            try:
                ts = datetime.fromtimestamp(ts_val).strftime("%Y-%m-%d %H:%M:%S.%f")[:-3]
            except (OSError, ValueError):
                pass

        level = fields.get("level", fields.get("log_level", fields.get("__LOG_LEVEL__", "")))
        content = fields.get("__CONTENT__", "")
        source = fields.get("__SOURCE__", "")

        # Build display line
        parts = []
        if ts:
            parts.append(f"{C.DIM}{ts}{C.NC}")
        if level:
            parts.append(color_level(level))
        if source:
            parts.append(f"{C.CYAN}{source}{C.NC}")

        # For structured logs, show key fields; for raw, show content
        skip_keys = {
            "__TIMESTAMP__", "__SOURCE__", "__CONTENT__", "__LOG_LEVEL__",
            "timestamp", "level", "log_level", "source", "time",
        }
        extra_fields = {
            k: v for k, v in fields.items()
            if k not in skip_keys and v and not k.startswith("__")
        }

        if content:
            parts.append(truncate(str(content)))
        if extra_fields:
            for k, v in list(extra_fields.items())[:10]:
                parts.append(f"{C.DIM}{k}={C.NC}{truncate(str(v))}")

        print(" | ".join(parts) if parts else json.dumps(fields, ensure_ascii=False))

    # Analysis results (SQL mode)
    if analysis_records:
        print(f"\n{C.BOLD}── Analysis Results ──{C.NC}")
        for record in analysis_records:
            print(json.dumps(record, ensure_ascii=False, indent=2))


def format_json(response_json: str):
    """Pretty-print the full JSON response."""
    try:
        obj = json.loads(response_json)
        print(json.dumps(obj, ensure_ascii=False, indent=2))
    except json.JSONDecodeError:
        print(response_json)


def format_raw(results: List[Dict], fields_filter: Optional[List[str]] = None):
    """One JSON object per line, with optional field filtering."""
    for log_item in results:
        if fields_filter:
            log_item = {k: log_item.get(k, "") for k in fields_filter if log_item.get(k, "") != ""}
        print(json.dumps(log_item, ensure_ascii=False))


# ── Core search ──────────────────────────────────────────────
def do_search(
    client: cls_client.ClsClient,
    topic_id: str,
    query: str,
    from_ms: int,
    to_ms: int,
    limit: int,
    sort: str,
    highlight: bool = False,
    use_new_analysis: bool = True,
    syntax_rule: int = 1,
    context: Optional[str] = None,
) -> models.SearchLogResponse:
    """Execute a single SearchLog API call."""
    req = models.SearchLogRequest()
    req.TopicId = topic_id
    req.Query = query
    req.From = from_ms
    req.To = to_ms
    req.SyntaxRule = syntax_rule
    req.Limit = limit
    req.Sort = sort
    req.HighLight = highlight
    req.UseNewAnalysis = use_new_analysis
    if context:
        req.Context = context
    return client.SearchLog(req)


def parse_results(response: models.SearchLogResponse) -> List[Dict]:
    """Extract log records from response into list of dicts."""
    results = []
    if response.Results:
        for log_info in response.Results:
            record = {}
            if log_info.LogJson:
                try:
                    record = json.loads(log_info.LogJson)
                except json.JSONDecodeError:
                    record = {"__CONTENT__": log_info.LogJson}
            results.append(record)
    return results


def search_all(
    client: cls_client.ClsClient,
    topic_id: str,
    query: str,
    from_ms: int,
    to_ms: int,
    limit: int,
    sort: str,
    highlight: bool,
    use_new_analysis: bool,
    syntax_rule: int,
    context: Optional[str],
    fetch_all: bool,
    output_format: str,
    fields_filter: Optional[List[str]] = None,
    quiet: bool = False,
):
    """Search with optional auto-pagination."""
    page = 0
    total_fetched = 0

    while True:
        page += 1
        try:
            resp = do_search(
                client, topic_id, query, from_ms, to_ms, limit, sort,
                highlight, use_new_analysis, syntax_rule, context,
            )
        except TencentCloudSDKException as e:
            print(f"{C.RED}ERROR: {e}{C.NC}", file=sys.stderr)
            sys.exit(1)

        if output_format == "json":
            format_json(resp.to_json_string())
            if not fetch_all or resp.ListOver:
                return
            context = resp.Context
            continue

        results = parse_results(resp)
        total_fetched += len(results)

        if output_format == "pretty":
            if page > 1 and not quiet:
                print(f"\n{C.DIM}── Page {page} ──{C.NC}", file=sys.stderr)
            analysis = None
            if resp.AnalysisRecords:
                analysis = []
                for ar in resp.AnalysisRecords:
                    try:
                        analysis.append(json.loads(ar.to_json_string()))
                    except (json.JSONDecodeError, AttributeError):
                        pass
            format_pretty(results, analysis, fields_filter)
        elif output_format == "raw":
            format_raw(results, fields_filter)

        # Check pagination
        if not fetch_all or resp.ListOver:
            break
        if not resp.Context:
            break
        context = resp.Context

        # Safety: avoid infinite loops (API max ~10000 logs with pagination)
        if page > 100:
            print(f"{C.YELLOW}WARNING: Reached 100 pages, stopping.{C.NC}", file=sys.stderr)
            break

    # Summary
    if not quiet:
        print(
            f"\n{C.GREEN}Total: {total_fetched} logs fetched ({page} page(s)){C.NC}",
            file=sys.stderr,
        )


# ── Argv preprocessing ───────────────────────────────────────
def preprocess_argv(argv: List[str]) -> List[str]:
    """Fix argparse issues with negative-looking values like '-1h'.

    Converts patterns like:
      --from -1h    →  --from=-1h
      --to -30m     →  --to=-30m
      --since -7d   →  --since=-7d
      --until -2h   →  --until=-2h
    """
    time_flags = {"--from", "--to", "--since", "--until"}
    result = []
    i = 0
    while i < len(argv):
        arg = argv[i]
        if arg in time_flags and i + 1 < len(argv):
            next_arg = argv[i + 1]
            # If next arg looks like a relative time (e.g., -1h, -30m)
            if re.match(r'^-\d+[hmds]$', next_arg):
                result.append(f"{arg}={next_arg}")
                i += 2
                continue
        result.append(arg)
        i += 1
    return result


# ── CLI ──────────────────────────────────────────────────────
def main():
    # Load .env before parsing args so env vars are available
    load_dotenv()

    # Preprocess argv to fix --from -1h issue
    argv = preprocess_argv(sys.argv[1:])

    parser = argparse.ArgumentParser(
        description="CLS Log Search Tool",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
EXAMPLES:
  # Query all logs in the last hour
  %(prog)s -t TOPIC_ID -q '*' -l 20

  # Query errors in last 30 minutes
  %(prog)s -t TOPIC_ID -q 'level:error' --since 30m

  # Query by time range
  %(prog)s -t TOPIC_ID -q 'level:error' --from '2026-03-17 10:00:00' --to '2026-03-17 12:00:00'

  # Query by UIN
  %(prog)s -t TOPIC_ID -q 'uin:12345 OR req.uin:12345'

  # SQL analysis
  %(prog)s -t TOPIC_ID -q '* | SELECT count(*) as cnt, level GROUP BY level'

  # Only show specific fields
  %(prog)s -t TOPIC_ID -q 'level:error' --fields msg,traceID,caller

  # Auto-paginate all results, quiet mode
  %(prog)s -t TOPIC_ID -q 'level:error' --all --quiet

CREDENTIAL PRIORITY:
  --secret-id/--secret-key  >  CLS_SECRET_ID/CLS_SECRET_KEY env  >  .env file

CQL QUICK REFERENCE:
  key:value              Exact match
  key:"phrase"           Phrase match
  key:val1 OR key:val2   OR condition
  key:val1 AND key:val2  AND condition
  NOT key:value          Negation
  key:>400               Range (>, >=, <, <=)
  key:*                  Field exists
  *                      Match all
  | SELECT ...           SQL analysis (after pipe)
""",
    )

    # ── Credential args ──
    parser.add_argument("--secret-id", default=None, help="Tencent Cloud SecretId (or CLS_SECRET_ID env)")
    parser.add_argument("--secret-key", default=None, help="Tencent Cloud SecretKey (or CLS_SECRET_KEY env)")

    # ── Connection args ──
    parser.add_argument("--region", default=None, help="Region (default: ap-guangzhou, or CLS_REGION env)")
    parser.add_argument("--endpoint", default=None, help="API endpoint (default: cls.internal.tencentcloudapi.com, or CLS_ENDPOINT env)")

    # ── Query args ──
    parser.add_argument("--topic", "-t", required=True, help="CLS Topic ID")
    parser.add_argument("--query", "-q", default="*", help="CQL query (default: '*')")
    # --from/--to: canonical names; --since/--until: natural aliases
    parser.add_argument("--from", "--since", dest="from_time", default=None,
                        help="Start time (default: 1h ago). Accepts: -1h, 30m, '2026-03-17 10:00:00', timestamp")
    parser.add_argument("--to", "--until", dest="to_time", default=None,
                        help="End time (default: now). Same formats as --from")
    parser.add_argument("--limit", "-l", type=int, default=100, help="Max logs per page (default: 100, max: 1000)")
    parser.add_argument("--sort", choices=["asc", "desc"], default="desc", help="Sort order (default: desc)")

    # ── Feature flags ──
    parser.add_argument("--highlight", action="store_true", help="Highlight matched keywords in results")
    parser.add_argument("--old-analysis", action="store_true", help="Use old analysis result format (default: new)")
    parser.add_argument("--syntax", type=int, choices=[0, 1], default=1, help="Syntax rule: 0=Lucene, 1=CQL (default: 1)")

    # ── Output args ──
    parser.add_argument("--format", "-f", dest="output_format", choices=["pretty", "json", "raw"], default="pretty",
                        help="Output format (default: pretty)")
    parser.add_argument("--fields", default=None,
                        help="Comma-separated field names to show (e.g., msg,traceID,level,time). Filters output to only these fields.")
    parser.add_argument("--context", default=None, help="Pagination context token from previous query")
    parser.add_argument("--all", "-a", action="store_true", help="Auto-paginate to fetch all results")
    parser.add_argument("--quiet", "--no-header", action="store_true",
                        help="Suppress stderr header/summary (cleaner for piped/programmatic use)")

    args = parser.parse_args(argv)

    # Parse fields filter
    fields_filter = None
    if args.fields:
        fields_filter = [f.strip() for f in args.fields.split(",") if f.strip()]

    # Clamp limit
    if args.limit > 1000:
        print(f"{C.YELLOW}WARNING: Limit clamped to 1000 (API max){C.NC}", file=sys.stderr)
        args.limit = 1000

    # Resolve connection config: CLI > env > default
    region = get_config(args.region, "CLS_REGION", "ap-guangzhou")
    endpoint = get_config(args.endpoint, "CLS_ENDPOINT", "cls.internal.tencentcloudapi.com")

    # Load credentials (3-level priority)
    secret_id, secret_key = load_credentials(args.secret_id, args.secret_key)

    # Parse times
    now_ms = int(time.time() * 1000)
    one_hour_ago_ms = int((time.time() - 3600) * 1000)
    from_ms = parse_time(args.from_time, one_hour_ago_ms)
    to_ms = parse_time(args.to_time, now_ms)

    # Print query info header (unless --quiet)
    if not args.quiet:
        from_dt = datetime.fromtimestamp(from_ms / 1000).strftime("%Y-%m-%d %H:%M:%S")
        to_dt = datetime.fromtimestamp(to_ms / 1000).strftime("%Y-%m-%d %H:%M:%S")
        print(f"{C.DIM}Topic:     {args.topic}{C.NC}", file=sys.stderr)
        print(f"{C.DIM}Query:     {args.query}{C.NC}", file=sys.stderr)
        print(f"{C.DIM}Range:     {from_dt} → {to_dt}{C.NC}", file=sys.stderr)
        info_parts = [f"Limit: {args.limit}", f"Sort: {args.sort}", f"Format: {args.output_format}"]
        if fields_filter:
            info_parts.append(f"Fields: {','.join(fields_filter)}")
        print(f"{C.DIM}{' | '.join(info_parts)}{C.NC}", file=sys.stderr)
        flags = []
        if args.highlight:
            flags.append("highlight")
        if args.syntax == 0:
            flags.append("Lucene")
        if flags:
            print(f"{C.DIM}Flags:     {', '.join(flags)}{C.NC}", file=sys.stderr)
        print(f"{C.DIM}Endpoint:  {endpoint} ({region}){C.NC}", file=sys.stderr)
        print(f"{C.DIM}{'─' * 60}{C.NC}", file=sys.stderr)

    # Create client
    cred = credential.Credential(secret_id, secret_key)
    http_profile = HttpProfile()
    http_profile.endpoint = endpoint
    client_profile = ClientProfile()
    client_profile.httpProfile = http_profile
    client = cls_client.ClsClient(cred, region, client_profile)

    # Execute search
    search_all(
        client=client,
        topic_id=args.topic,
        query=args.query,
        from_ms=from_ms,
        to_ms=to_ms,
        limit=args.limit,
        sort=args.sort,
        highlight=args.highlight,
        use_new_analysis=not args.old_analysis,
        syntax_rule=args.syntax,
        context=args.context,
        fetch_all=args.all,
        output_format=args.output_format,
        fields_filter=fields_filter,
        quiet=args.quiet,
    )


if __name__ == "__main__":
    main()
