#!/usr/bin/env python3
"""只读检查 Monitor 的 SQLite（在控制机上运行，由 monitor-db-check.sh 经 SSH 送过去执行）。

这是 moox-cli doctor alerts 的补充深挖，回答“首页为什么提示有问题需要处理”时概览之外的几个问题：哪些告警还没恢复、最近一次检查结果是什么、
检查表里各类检查有多少、哪些视图的指标序列静默了、最近的告警事件。
只用标准库，以只读方式打开数据库，不写任何东西。
"""
import datetime
import os
import sqlite3
import sys

DB = os.environ.get("MOOX_MONITOR_DB", "/data/moox/data/monitor/monitor.db")
LOG = os.environ.get("MOOX_MONITOR_LOG", "/data/moox/logs/monitor/trpc.log")
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

    section("未恢复的告警（检查表里 c_alert_status = firing）")
    rows = list(db.execute(
        "SELECT c_space_id, c_check_id, c_severity, c_triggered_at, c_status, c_reason, c_failure_reason, c_checked_at "
        "FROM t_monitor_checks WHERE c_alert_status = 'firing' ORDER BY c_triggered_at"
    ))
    if not rows:
        print("没有。")
    for space, check_id, severity, triggered, status, reason, failure, checked in rows:
        current = reason if status == "down" else f"已通过、等待恢复确认（最近失败：{failure}）"
        print(f"- {space}/{check_id} [{severity}] 触发于 {ago(triggered, now)}；最近一次检查：{current}（{ago(checked, now)}）")

    section("检查概况（按种类 / 最近结果）")
    for kind, status, count in db.execute(
        "SELECT substr(c_check_id, 1, instr(c_check_id || ':', ':') - 1), c_status, COUNT(*) FROM t_monitor_checks GROUP BY 1, 2 ORDER BY 1, 2"
    ):
        print(f"- {kind} · {status}：{count}")
    print("检查由评估按本轮事实生成，不再产出的当轮删除（在告警的会写一条恢复事件），没有要手工清理的残留。")

    section("最近的告警事件")
    events = list(db.execute(
        "SELECT c_created_at, c_event_type, c_space_id, c_check_id, c_title FROM t_monitor_alert_events "
        "ORDER BY c_created_at DESC, c_id DESC LIMIT 10"
    ))
    if not events:
        print("没有。")
    for created, event_type, space, check_id, title in events:
        print(f"- {ago(created, now)} {event_type} {space}/{check_id}（{title}）")

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

    section("Monitor 日志里的告警推送失败（最近几条）")
    if os.path.exists(LOG):
        lines = [l.rstrip() for l in open(LOG, encoding="utf-8", errors="replace")
                 if "send alert notification" in l or "alert notification partially failed" in l]
        for line in lines[-5:]:
            print(line[:400])
        if not lines:
            print("没有。")
    else:
        print(f"找不到日志 {LOG}（用 MOOX_MONITOR_LOG 指定）。")


if __name__ == "__main__":
    main()
