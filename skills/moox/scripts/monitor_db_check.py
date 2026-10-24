#!/usr/bin/env python3
"""只读检查 Monitor 的 SQLite（在控制机上运行，由 monitor-db-check.sh 经 SSH 送过去执行）。

这是 moox-cli doctor alerts 的补充深挖，回答“首页为什么提示有问题需要处理”时概览之外的几个问题：哪些告警还没恢复、最近一次检查结果是什么、
有多少失效的自动检查还没被清理、哪些视图的指标序列静默了、清理日志有没有跑。
只用标准库，以只读方式打开数据库，不写任何东西。
"""
import datetime
import os
import sqlite3
import sys

DB = os.environ.get("MOOX_MONITOR_DB", "/data/moox/prod/current/monitor/data/monitor/monitor.db")
LOG = os.environ.get("MOOX_MONITOR_LOG", "/data/moox/prod/current/monitor/logs/trpc.log")
RETIRED = "已停用或移除，不再检查"
NO_DATA_AFTER = datetime.timedelta(minutes=2)


def parse(ts):
    if not ts:
        return None
    ts = ts.replace("Z", "+00:00")
    if "." in ts:
        head, rest = ts.split(".", 1)
        frac = rest
        tz = ""
        for sign in ("+", "-"):
            if sign in rest:
                frac, tz = rest.split(sign, 1)
                tz = sign + tz
                break
        ts = head + "." + frac[:6].ljust(6, "0") + tz
    try:
        parsed = datetime.datetime.fromisoformat(ts)
    except ValueError:
        return None
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=datetime.timezone.utc)
    return parsed.astimezone(datetime.timezone.utc)


def ago(ts, now):
    parsed = parse(ts)
    if not parsed:
        return "-"
    seconds = int((now - parsed).total_seconds())
    if seconds < 90:
        return f"{seconds} 秒前"
    if seconds < 5400:
        return f"{seconds // 60} 分钟前"
    if seconds < 172800:
        return f"{seconds // 3600} 小时前"
    return f"{seconds // 86400} 天前"


def section(title):
    print(f"\n== {title}")


def main():
    if not os.path.exists(DB):
        sys.exit(f"错误：找不到 Monitor 数据库 {DB}（用 MOOX_MONITOR_DB 指定）")
    db = sqlite3.connect(f"file:{DB}?mode=ro", uri=True)
    now = datetime.datetime.now(datetime.timezone.utc)

    section("未恢复的告警状态（状态不是 ok / resolved）")
    rows = list(db.execute(
        "SELECT c_rule_id, c_check_id, c_status, c_triggered_at FROM t_monitor_alert_states "
        "WHERE lower(c_status) NOT IN ('ok', 'resolved') ORDER BY c_triggered_at"
    ))
    if not rows:
        print("没有。")
    for rule_id, check_id, status, triggered in rows:
        latest = db.execute(
            "SELECT c_error_message, c_checked_at FROM t_monitor_check_results WHERE c_check_id = ? "
            "ORDER BY c_checked_at DESC, c_id DESC LIMIT 1", (check_id,)
        ).fetchone()
        message = f"{latest[0]}（{ago(latest[1], now)}）" if latest else "没有检查结果"
        print(f"- {rule_id} [{status}] 触发于 {ago(triggered, now)}；最近一次检查：{message}")

    section("检查概况（启用 / 来源 / 分组）")
    for enabled, source, group, count in db.execute(
        "SELECT c_enabled, c_source, c_group_name, COUNT(*) FROM t_monitor_checks GROUP BY 1, 2, 3 ORDER BY 1 DESC, 2, 3"
    ):
        print(f"- {'启用' if enabled else '停用'} · {source} · {group}：{count}")

    section("已失效但还没被清理的自动业务检查")
    stale = 0
    for check_id, in db.execute(
        "SELECT c_check_id FROM t_monitor_checks WHERE c_enabled = 1 AND c_source = 'observability' AND c_group_name = 'business'"
    ).fetchall():
        latest = db.execute(
            "SELECT c_error_message FROM t_monitor_check_results WHERE c_check_id = ? ORDER BY c_checked_at DESC, c_id DESC LIMIT 1",
            (check_id,),
        ).fetchone()
        if latest and latest[0] == RETIRED:
            stale += 1
            print(f"- {check_id}")
    if not stale:
        print("没有。")
    else:
        print(f"共 {stale} 个；清理定时器每 5 分钟跑一次，持续“不再检查”超过 1 小时且没有未恢复告警的会被清掉。")

    section("检查已不存在的默认告警规则（主机规则除外）")
    orphans = [r[0] for r in db.execute(
        "SELECT c_rule_id FROM t_monitor_alert_rules WHERE c_check_id NOT IN (SELECT c_check_id FROM t_monitor_checks) "
        "AND c_check_id NOT LIKE 'host:%'"
    )]
    print("没有。" if not orphans else "\n".join(f"- {r}" for r in orphans))
    if orphans:
        print("清理与默认规则补齐可能同时发生，规则会被补建出来；清理定时器每 5 分钟清掉存在超过 10 分钟的孤儿规则，等一两轮即可。")

    section("静默超过 2 分钟的视图输出水位序列")
    quiet = 0
    try:
        series = list(db.execute(
            "SELECT c_labels_json, c_last_seen_at FROM t_monitor_metric_series "
            "WHERE c_metric_name = 'moox_storage_view_output_watermark_timestamp_seconds' ORDER BY c_last_seen_at"
        ))
    except sqlite3.OperationalError:
        series = []
        print("没有指标序列表（Monitor 版本不同？）。")
    for labels, last_seen in series:
        seen = parse(last_seen)
        if seen and now - seen > NO_DATA_AFTER:
            quiet += 1
            print(f"- {labels}：最后上报 {ago(last_seen, now)}")
    if not quiet and series:
        print("没有。")
    elif quiet:
        print("周期不短于 5 分钟的视图容忍一个周期加 5 分钟；分钟级视图静默超过 2 分钟会判停止上报。"
              "最后上报停在很久以前的多半是已删除或停用的视图。")

    section("清理日志（最近几条）")
    if os.path.exists(LOG):
        lines = [l.rstrip() for l in open(LOG, encoding="utf-8", errors="replace")
                 if "monitor_stale_checks_pruned" in l or "monitor_orphan_rules_pruned" in l]
        for line in lines[-5:]:
            print(line[:400])
        if not lines:
            print("日志里还没有清理记录。")
    else:
        print(f"找不到日志 {LOG}（用 MOOX_MONITOR_LOG 指定）。")


if __name__ == "__main__":
    main()
