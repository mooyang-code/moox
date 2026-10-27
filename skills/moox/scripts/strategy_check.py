#!/usr/bin/env python3
"""迁移策略上线后的验证工具（只依赖 Python 标准库，对生产只读）。用法见 references/strategy-migration.md。

子命令：
  targets    导出策略实例每期的目标权重（只取 ok 的期）→ JSON {名称: {bar_end: {标的: 权重}}}
  summary    各实例的结果状态、跳过原因与规则状态体积（稳定性验收）
  compare    把 targets 的结果与参考框架算出的权重 CSV（bar_end,instrument_id,weight）逐期比对（语义一致验收）
  latency    按 bar_end 关联因子引擎与策略日志，给出端到端分段耗时的 p50 / p95（冷启动与稳态分开）
  task-lag   因子任务从收盘到完成的耗时，按是否落在边界分钟分组（对照其他任务有没有被拖慢）

数据来源：
  - 策略库：--db 指定本地 SQLite；不指定时经 SSH 只读打开控制机上的生产库（主机取 --control 或 ~/.moox-release.env 的 MOOX_RELEASE_HOST_CONTROL）。
  - 日志：--engine-log / --strategy-log 指定本地文件；不指定时经 SSH 只抓取相关行（因子机用 --factor-host，控制机同上）。
  - 日志时间戳是主机本地时间，用 --log-tz 指定时区（生产为 +08:00）。
"""
import argparse
import csv
import datetime as dt
import json
import os
import re
import subprocess
import sys
from collections import Counter
from decimal import ROUND_DOWN, Decimal

REMOTE_DB = '$(readlink -f /data/moox/prod/current)/strategy/data/strategy.sqlite'
ENGINE_LOG = '/data/moox/factor-engine/logs/factor-engine.log'
STRATEGY_LOG = '/data/moox/prod/current/strategy/logs/trpc.log'
UTC = dt.timezone.utc

# 在策略库上执行的只读查询；本地直接执行，远程经 SSH 交给控制机上的 python3。
DB_SCRIPT = r'''
import json, sqlite3, sys
db, mode, ids = sys.argv[1], sys.argv[2], sys.argv[3:]
c = sqlite3.connect("file:%s?mode=ro" % db, uri=True)
out = {}
for iid in ids:
    rows = []
    for bar, status, reason, targets, states in c.execute(
            "SELECT c_bar_end_time, c_status, c_skip_reason, c_targets_json, c_rule_states_json "
            "FROM t_strategy_results WHERE c_instance_id = ? ORDER BY c_bar_end_time", (iid,)):
        item = {"bar": bar, "status": status, "reason": reason, "state_bytes": len(states or "")}
        if mode == "targets" and status == "ok":
            item["targets"] = {t["instrument_id"]: t["target_weight"] for t in json.loads(targets or "[]")}
        rows.append(item)
    out[iid] = rows
print(json.dumps(out))
'''


def fail(message):
    raise SystemExit(f'strategy_check: {message}')


def release_host(name):
    path = os.path.expanduser('~/.moox-release.env')
    if os.path.exists(path):
        for line in open(path, encoding='utf-8'):
            if line.startswith(name + '='):
                return line.split('=', 1)[1].strip().strip('"\'')
    return ''


def ssh(host, command, stdin=None):
    if not host:
        fail('没有主机：用参数指定，或在 ~/.moox-release.env 里配置')
    done = subprocess.run(['ssh', '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=15', host, command],
                          input=stdin, capture_output=True, text=True)
    if done.returncode not in (0, 1):  # grep 没有匹配时返回 1
        fail(f'ssh {host} 失败：{done.stderr.strip()}')
    return done.stdout


def parse_pairs(values, label):
    out = {}
    for value in values or []:
        if '=' not in value:
            fail(f'{label} 的格式是 名称=值：{value}')
        name, rest = value.split('=', 1)
        out[name] = rest
    if not out:
        fail(f'至少给一个 {label}')
    return out


def query_db(args, mode, ids):
    if args.db:
        done = subprocess.run([sys.executable, '-I', '-', args.db, mode, *ids], input=DB_SCRIPT, capture_output=True, text=True)
        if done.returncode:
            fail(f'读取 {args.db} 失败：{done.stderr.strip()}')
        return json.loads(done.stdout)
    host = args.control or release_host('MOOX_RELEASE_HOST_CONTROL')
    quoted = ' '.join("'%s'" % i for i in ids)
    return json.loads(ssh(host, f'python3 - "{REMOTE_DB}" {mode} {quoted}', stdin=DB_SCRIPT))


def iso(ms):
    return dt.datetime.fromtimestamp(ms / 1000, UTC).strftime('%Y-%m-%dT%H:%M:%SZ')


def parse_time(text):
    return dt.datetime.fromisoformat(text.replace('Z', '+00:00')).astimezone(UTC)


def pct(values, q):
    values = sorted(values)
    if not values:
        return float('nan')
    k = (len(values) - 1) * q
    lo, hi = int(k), min(int(k) + 1, len(values) - 1)
    return values[lo] + (values[hi] - values[lo]) * (k - lo)


# ---------------------------------------------------------------- targets / summary


def cmd_targets(args):
    instances = parse_pairs(args.instance, '--instance')
    data = query_db(args, 'targets', list(instances.values()))
    out = {name: {iso(r['bar']): r['targets'] for r in data[iid] if r['status'] == 'ok'} for name, iid in instances.items()}
    text = json.dumps(out, ensure_ascii=False, sort_keys=True)
    if args.output:
        with open(args.output, 'w', encoding='utf-8') as f:
            f.write(text + '\n')
    else:
        print(text)
    print(' '.join(f'{name} {len(v)} 期' for name, v in out.items()), file=sys.stderr)


def cmd_summary(args):
    instances = parse_pairs(args.instance, '--instance')
    data = query_db(args, 'summary', list(instances.values()))
    for name, iid in instances.items():
        rows = data[iid]
        if not rows:
            print(f'{name}：没有结果')
            continue
        status = Counter(r['status'] for r in rows)
        reasons = Counter(r['reason'] for r in rows if r['status'] != 'ok')
        sizes = [r['state_bytes'] for r in rows]
        tail = sizes[-min(len(sizes), args.plateau):]
        print(f"{name}：{len(rows)} 期（{iso(rows[0]['bar'])} ～ {iso(rows[-1]['bar'])}），状态 {dict(status)}，跳过原因 {dict(reasons) or '无'}")
        print(f'  规则状态字节：首期 {sizes[0]}，最大 {max(sizes)}，末期 {sizes[-1]}；'
              f'最近 {len(tail)} 期波动 {max(tail) - min(tail)}；超过 {args.state_limit} 的期数 {sum(1 for s in sizes if s > args.state_limit)}')


# ---------------------------------------------------------------- compare


def truncate(value, step):
    """与 moox 一致按十进制定点向零截断（浮点除法会把 0.7284 截成 0.7283）。"""
    if not step:
        return value
    return float(Decimal(repr(value)).quantize(Decimal(repr(step)), rounding=ROUND_DOWN))


def read_reference(path, trunc):
    out = {}
    with open(path, newline='', encoding='utf-8') as f:
        for row in csv.DictReader(f):
            bar = parse_time(row['bar_end']).strftime('%Y-%m-%dT%H:%M:%SZ')
            value = truncate(float(row['weight']), trunc)
            if value != 0:
                out.setdefault(bar, {})[row['instrument_id']] = value
    return out


def cmd_compare(args):
    targets = json.load(open(args.targets, encoding='utf-8'))
    if args.name not in targets:
        fail(f'{args.targets} 里没有 {args.name}')
    moox = {bar: {k: float(v) for k, v in w.items() if float(v) != 0} for bar, w in targets[args.name].items()}
    ref = read_reference(args.reference, args.trunc)
    since = parse_time(args.since) if args.since else None
    until = parse_time(args.until) if args.until else None
    bars = sorted(b for b in set(moox) | set(ref)
                  if (since is None or parse_time(b) >= since) and (until is None or parse_time(b) <= until))
    if args.until is None and ref:
        last_ref = max(ref)
        bars = [b for b in bars if b <= last_ref]  # 参考数据只覆盖到它的最后一期
    report, worst, bad, prev = [], 0.0, 0, None
    changed = shrink = nonempty = 0
    for bar in bars:
        m, r = moox.get(bar), ref.get(bar, {})
        if m is None:
            report.append(f'  {bar[11:16]} moox 没有 ok 结果（参考 {len(r)} 个）  ←')
            bad += 1
            continue
        ids = set(m) | set(r)
        diff = max((abs(m.get(i, 0) - r.get(i, 0)) for i in ids), default=0.0)
        worst = max(worst, diff)
        ok = set(m) == set(r) and diff <= args.atol
        bad += 0 if ok else 1
        nonempty += 1 if m else 0
        if prev is not None:
            changed += 1 if m != prev else 0
            shrink += 1 if len(m) < len(prev) else 0
        prev = m
        line = f'  {bar[11:16]} moox {len(m)} 个 / 参考 {len(r)} 个，最大差 {diff:.6g}'
        if not ok:
            line += f'  只在 moox：{sorted(set(m) - set(r))[:3]} 只在参考：{sorted(set(r) - set(m))[:3]}  ←'
        report.append(line)
    print(f'{args.name}：{len(bars)} 期，不一致 {bad} 期，最大权重差 {worst:.6g}；'
          f'有持仓 {nonempty} 期，权重变化 {changed} 期，持仓数减少 {shrink} 期')
    if args.table or bad:
        print('\n'.join(report))
    if not bars:
        fail('窗口里没有可比的期')
    if bad:
        raise SystemExit(1)
    if nonempty == 0 or changed == 0:
        print('警告：窗口里没有持仓或权重从未变化，开仓 / 到期路径没有被检验到，需要更长或更活跃的窗口', file=sys.stderr)
        raise SystemExit(2)


# ---------------------------------------------------------------- 日志


def go_seconds(text):
    """解析 Go time.Duration 的字符串（1h2m3.5s、250ms、12µs、0s）。"""
    text = text.strip()
    if text in ('0', '0s'):
        return 0.0
    total, pos = 0.0, 0
    units = {'h': 3600, 'm': 60, 's': 1, 'ms': 1e-3, 'us': 1e-6, 'µs': 1e-6, 'μs': 1e-6, 'ns': 1e-9}
    for number, unit in re.findall(r'([\d.]+)(ms|us|µs|μs|ns|h|m|s)', text):
        total += float(number) * units[unit]
        pos += len(number) + len(unit)
    if pos != len(text):
        fail(f'无法解析时长：{text}')
    return total


def log_time(text, tz):
    return dt.datetime.strptime(text, '%Y-%m-%d %H:%M:%S.%f').replace(tzinfo=tz).astimezone(UTC)


def parse_tz(text):
    m = re.fullmatch(r'([+-])(\d\d):(\d\d)', text)
    if not m:
        fail(f'--log-tz 的格式是 +08:00：{text}')
    delta = dt.timedelta(hours=int(m.group(2)), minutes=int(m.group(3)))
    return dt.timezone(delta if m.group(1) == '+' else -delta)


ENGINE_START = re.compile(r'(\S+ \S+)\s.*factor_period_start task_id=(\S+) period_time=(\S+)')
ENGINE_DONE = re.compile(r'(\S+ \S+)\s.*factor_period_done task_id=(\S+) period_time=(\S+) status=(\S+) .*?duration=(\S+)(.*)')
STRATEGY_TIMING = re.compile(r'(\S+ \S+)\s.*实例 (\S+) 周期 (\S+) (\S+) 耗时：K线至就绪=(\S+) 就绪等待=(\S+) 读取=(\S+)（.*?） '
                             r'求值=(\S+) 提交=(\S+) 本期处理=(\S+) 总计=(\S+)')


def engine_lines(args, tasks):
    if args.engine_log:
        return open(args.engine_log, encoding='utf-8', errors='replace').read()
    pattern = '|'.join(re.escape(t) for t in tasks)
    return ssh(args.factor_host, f"grep -a -E 'factor_period_(start|done) task_id=({pattern}) ' {args.engine_log_path}")


def parse_engine(text, tz):
    starts, done = {}, {}
    for line in text.splitlines():
        m = ENGINE_START.match(line)
        if m:
            starts[(m.group(2), m.group(3))] = log_time(m.group(1), tz)
            continue
        m = ENGINE_DONE.match(line)
        if m:
            stages = {k: go_seconds(v) for k, v in re.findall(r'\b(plan|load|compute|assemble|write|report)=(\S+)', m.group(6))}
            done[(m.group(2), m.group(3))] = {'at': log_time(m.group(1), tz), 'status': m.group(4),
                                              'duration': go_seconds(m.group(5)), **stages}
    return starts, done


def cmd_latency(args):
    pairs = {}
    for name, value in parse_pairs(args.pair, '--pair').items():
        if ':' not in value:
            fail(f'--pair 的格式是 名称=实例ID:因子任务ID：{value}')
        pairs[name] = tuple(value.split(':', 1))
    tz = parse_tz(args.log_tz)
    starts, done = parse_engine(engine_lines(args, [t for _, t in pairs.values()]), tz)
    if args.strategy_log:
        text = open(args.strategy_log, encoding='utf-8', errors='replace').read()
    else:
        ids = '|'.join(re.escape(i) for i, _ in pairs.values())
        text = ssh(args.control or release_host('MOOX_RELEASE_HOST_CONTROL'),
                   f"grep -a '耗时：' {args.strategy_log_path} | grep -a -E '{ids}'")
    timing = {}
    for line in text.splitlines():
        m = STRATEGY_TIMING.match(line)
        if m:
            v = [go_seconds(m.group(i)) for i in range(5, 12)]
            timing[(m.group(2), m.group(3))] = {'status': m.group(4), 'ready': v[0], 'wait': v[1], 'read': v[2],
                                                'eval': v[3], 'commit': v[4], 'total': v[6]}
    since = parse_time(args.since)
    until = parse_time(args.until) if args.until else None
    step = dt.timedelta(minutes=args.bar_minutes)
    keys = ['收盘→因子开算', '因子 load', '因子 compute', '因子 写出', '因子合计', '因子完成→就绪', '就绪等待', '读取', '求值', '总计']
    print(f'统计区间：bar_end ≥ {since:%Y-%m-%d %H:%M}Z' + (f'，≤ {until:%H:%M}Z' if until else '') + '；单位秒；p50 / p95')
    for name, (instance, task) in pairs.items():
        rows = []
        for (iid, bar_text), r in timing.items():
            if iid != instance:
                continue
            bar_end = parse_time(bar_text)
            if bar_end < since or (until and bar_end > until):
                continue
            period = (bar_end - step).strftime('%Y-%m-%dT%H:%M:%SZ')
            s, d = starts.get((task, period)), done.get((task, period))
            if not s or not d:
                continue
            ready_at = bar_end + dt.timedelta(seconds=r['ready'])
            rows.append({'bar_end': bar_end, 'status': r['status'],
                         '收盘→因子开算': (s - bar_end).total_seconds(),
                         '因子 load': d.get('load', 0.0), '因子 compute': d.get('compute', 0.0),
                         '因子 写出': d.get('assemble', 0.0) + d.get('write', 0.0) + d.get('report', 0.0),
                         '因子合计': (d['at'] - s).total_seconds(), '因子完成→就绪': (ready_at - d['at']).total_seconds(),
                         '就绪等待': r['wait'], '读取': r['read'], '求值': r['eval'], '总计': r['total']})
        rows.sort(key=lambda x: x['bar_end'])
        cold, steady = rows[:args.cold], rows[args.cold:]
        print(f'\n== {name}（实例 {instance}，任务 {task}）：冷启动 {len(cold)} 期，稳态 {len(steady)} 期，'
              f'非 ok {sum(1 for x in rows if x["status"] != "ok")} 期')
        for label, group in (('冷启动', cold), ('稳态', steady)):
            if group:
                print(f'  {label}：' + '；'.join(f'{k} {pct([x[k] for x in group], .5):.2f}/{pct([x[k] for x in group], .95):.2f}' for k in keys))
        if args.table:
            for x in rows:
                print('   ', x['bar_end'].strftime('%m-%d %H:%M'), x['status'], ' '.join(f'{k}={x[k]:.2f}' for k in keys))
        if len(steady) < args.min_steady:
            print(f'  注意：稳态只有 {len(steady)} 期，验收要求至少 {args.min_steady} 期')


def cmd_task_lag(args):
    tz = parse_tz(args.log_tz)
    _, done = parse_engine(engine_lines(args, args.task), tz)
    since = parse_time(args.since) if args.since else None
    until = parse_time(args.until) if args.until else None
    excludes = []
    for item in args.exclude or []:
        a, b = item.split(',', 1)
        excludes.append((parse_time(a), parse_time(b)))
    step = dt.timedelta(minutes=args.bar_minutes)
    for task in args.task:
        groups = {'边界分钟': [], '其余分钟': []}
        for (t, period_text), d in done.items():
            if t != task:
                continue
            period = parse_time(period_text)
            if (since and period < since) or (until and period > until) or any(a <= period <= b for a, b in excludes):
                continue
            close = period + step
            on_boundary = args.boundary and (close.hour * 60 + close.minute) % args.boundary == 0
            groups['边界分钟' if on_boundary else '其余分钟'].append(((d['at'] - close).total_seconds(), d['status'], d.get('compute', 0.0)))
        for label, g in groups.items():
            if not g:
                continue
            lags = [x[0] for x in g]
            status = Counter(x[1] for x in g)
            print(f'{task} {label}（收盘落在 {args.boundary} 分钟边界）' if label == '边界分钟' else f'{task} {label}',
                  f'{len(g)} 期，状态 {dict(status)}；收盘→完成 p50 {pct(lags, .5):.2f} p95 {pct(lags, .95):.2f} max {max(lags):.2f}；'
                  f'compute p50 {pct([x[2] for x in g], .5):.2f}')


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest='cmd', required=True)

    def db_args(p):
        p.add_argument('--instance', action='append', help='名称=实例ID，可重复')
        p.add_argument('--db', help='本地策略库 SQLite；不给时经 SSH 只读打开生产库')
        p.add_argument('--control', help='控制机 SSH 目标；默认取 ~/.moox-release.env')

    def log_args(p):
        p.add_argument('--engine-log', help='本地因子引擎日志；不给时经 SSH 从 --factor-host 抓取')
        p.add_argument('--engine-log-path', default=ENGINE_LOG)
        p.add_argument('--factor-host', help='因子机 SSH 目标（因子机不在发布主机清单里，要显式给）')
        p.add_argument('--log-tz', default='+08:00', help='日志时间戳的时区')
        p.add_argument('--bar-minutes', type=int, default=5, help='周期长度（分钟）；bar_end = period_time + 周期')

    p = sub.add_parser('targets', help='导出每期目标权重')
    db_args(p)
    p.add_argument('--output')
    p.set_defaults(func=cmd_targets)

    p = sub.add_parser('summary', help='结果状态、跳过原因与状态体积')
    db_args(p)
    p.add_argument('--plateau', type=int, default=12, help='看最近多少期的状态体积波动')
    p.add_argument('--state-limit', type=int, default=256 * 1024)
    p.set_defaults(func=cmd_summary)

    p = sub.add_parser('compare', help='与参考框架的权重逐期比对')
    p.add_argument('--targets', required=True, help='targets 子命令的输出')
    p.add_argument('--name', required=True, help='targets 里的名称')
    p.add_argument('--reference', required=True, help='参考权重 CSV：bar_end（K 线收盘，RFC3339 UTC）,instrument_id,weight')
    p.add_argument('--since')
    p.add_argument('--until')
    p.add_argument('--trunc', type=float, default=0.0, help='参考权重按此粒度向零截断（与 moox 输出精度一致，例如 1e-4）')
    p.add_argument('--atol', type=float, default=1e-9)
    p.add_argument('--table', action='store_true')
    p.set_defaults(func=cmd_compare)

    p = sub.add_parser('latency', help='端到端分段耗时')
    log_args(p)
    p.add_argument('--pair', action='append', help='名称=实例ID:因子任务ID，可重复')
    p.add_argument('--strategy-log', help='本地策略日志；不给时经 SSH 从控制机抓取')
    p.add_argument('--strategy-log-path', default=STRATEGY_LOG)
    p.add_argument('--control')
    p.add_argument('--since', required=True, help='第一根 bar_end（UTC）')
    p.add_argument('--until')
    p.add_argument('--cold', type=int, default=3, help='启用后前几期算冷启动')
    p.add_argument('--min-steady', type=int, default=48)
    p.add_argument('--table', action='store_true')
    p.set_defaults(func=cmd_latency)

    p = sub.add_parser('task-lag', help='因子任务收盘到完成的耗时')
    log_args(p)
    p.add_argument('--task', action='append', required=True, help='因子任务 ID，可重复')
    p.add_argument('--since')
    p.add_argument('--until')
    p.add_argument('--boundary', type=int, default=5, help='边界分钟（与新任务同时开算的那一分钟），0 表示不分组')
    p.add_argument('--exclude', action='append', help='排除的 period_time 区间：开始,结束（UTC），可重复；排除时在报告里写明原因')
    p.set_defaults(func=cmd_task_lag)

    args = ap.parse_args()
    args.func(args)


if __name__ == '__main__':
    main()
