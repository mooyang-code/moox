"""proto 时间字段写法检查（3H 字段清单 1.2 节）。由 check-proto-time-fields.sh 调用。

规则：
  ① 禁止 google.protobuf.Timestamp（import 与字段类型）；
  ② 禁止以 _at 结尾的 string 字段（甲类例外表除外）；
  ③ 名字像时间的 int64 字段不在乙类白名单里的视为 Unix 毫秒，注释（字段上方紧挨的注释与行尾注释）里不得出现“秒”（“毫秒”除外，含“Unix 秒”“纳秒”）；
  ④ 字段名不得以 _at_ms、_time_ms、_unix、_unix_nano 结尾。
规则①总对全部 proto 生效；规则②～④只对 strict 范围内的文件生效（见 check-proto-time-fields.sh）。
"""

import re
import sys

# 乙类白名单：(文件名后缀或 *, 消息名或 *, 字段名)。这些 int64 是数据坐标的 Unix 秒，新增乙类字段要改这里。
SECONDS_WHITELIST = [
    ("*", "*", "period_time"),
    ("*", "PrimaryRegisterSeriesSnapshotReq", "effective_from"),
    ("*", "*", "last_period_time"),
    ("*", "*", "last_submitted_period"),
    ("*", "*", "period_times"),
    ("*", "WriteSeriesReq", "times"),
    ("*", "StreamPeriodStat", "period"),
    ("factor.proto", "TimeRange", "start"),
    ("factor.proto", "TimeRange", "end"),
    ("storage_events.proto", "CollectorKlineBackfilled", "start"),
    ("storage_events.proto", "CollectorKlineBackfilled", "end"),
    ("storage_events.proto", "CollectorKlineAbsent", "start"),
    ("storage_events.proto", "CollectorKlineAbsent", "end"),
]

# 甲类例外表：以 _at 结尾、但值是 data_time 文本的字段（规则②不查）。
DATA_TIME_TEXT_EXCEPTIONS = [
    ("monitor.proto", "*", "input_watermark_at"),
    ("monitor.proto", "*", "output_watermark_at"),
    ("monitor.proto", "*", "expected_bar_at"),
]

# 名字以 _ms 结尾的时长（相对收盘的毫秒数、计算耗时），规则③不查。
DURATION_EXCEPTIONS = [
    ("collector.proto", "StreamPeriodStat", "last_arrival_ms"),
    ("collector.proto", "StreamPeriodStat", "commit_done_ms"),
    ("collector.proto", "StreamPeriodStat", "commit_ms"),
    ("factor.proto", "StepReplayRsp", "compute_ms"),
]

TIME_NAME = re.compile(
    r"time|_at$|^at_|_ms$|_ts$|timestamp|date|since|until|deadline|expir|^start|start$|^end$|_end$|end_|begin"
    r"|_from$|^from|_to$|^to$|^first_|seen|heartbeat|day$|^day|clock|bar_|period|watermark|progress_|unix|nano|epoch|_on$"
)
DURATION_NAME = re.compile(r"seconds|duration|interval|timeout|ttl|lag|offset|reserve|lookback|_days$|per_second|wait_ms")
INT64_TYPES = {"int64", "sint64", "sfixed64", "uint64", "fixed64"}
FORBIDDEN_SUFFIX = re.compile(r"(_at_ms|_time_ms|_unix|_unix_nano)$")
SECONDS_IN_COMMENT = re.compile(r"(?<!毫)秒")
FIELD = re.compile(r"\s*(repeated\s+|optional\s+)?(map<[^>]*>|[\w.]+)\s+(\w+)\s*=\s*(\d+)")


def matches(table, path, message, field):
    leaf = message.split(".")[-1]
    for file_suffix, msg, name in table:
        if name == field and (msg == "*" or msg == leaf) and (file_suffix == "*" or path.endswith("/" + file_suffix) or path == file_suffix):
            return True
    return False


def parse(path):
    """逐个字段返回 (行号, 消息路径, 类型, 名字, 注释)。注释是字段上方紧挨的 // 注释加行尾注释。"""
    stack, pending, out = [], [], []
    buf = ""
    for lineno, line in enumerate(open(path, encoding="utf-8").read().split("\n"), 1):
        code = line.split("//")[0]
        trailing = line[len(code):].strip()
        if not code.strip():
            # 只有注释的行累积成字段上方的注释块；空行把注释块断开。
            pending = pending + [trailing] if trailing else []
            continue
        comment = " ".join(pending + ([trailing] if trailing else []))
        pending = []
        for token in re.split(r"([{}])", code):
            if token == "{":
                m = re.search(r"\b(message|enum|oneof|service)\s+(\w+)\s*$", buf)
                stack.append(m.group(2) if m else "?")
                buf = ""
            elif token == "}":
                if stack:
                    stack.pop()
                buf = ""
            else:
                buf += token
                parts = buf.split(";")
                for statement in parts[:-1]:
                    fm = FIELD.match(statement)
                    if fm and stack:
                        out.append((lineno, ".".join(stack), fm.group(2), fm.group(3), comment))
                buf = parts[-1]
    return out


def main(argv):
    strict = set()
    files = []
    mode = None
    for arg in argv:
        if arg == "--strict":
            mode = "strict"
        elif arg == "--files":
            mode = "files"
        elif mode == "strict":
            strict.add(arg)
        else:
            files.append(arg)
    violations = []
    for path in files:
        text = open(path, encoding="utf-8").read()
        if re.search(r'import\s+"google/protobuf/timestamp\.proto"', text):
            violations.append(f"{path}: ① 不得导入 google/protobuf/timestamp.proto，时刻用 int64 Unix 毫秒")
        for lineno, message, ftype, name, comment in parse(path):
            where = f"{path}:{lineno} {message}.{name}"
            if ftype in ("google.protobuf.Timestamp", ".google.protobuf.Timestamp"):
                violations.append(f"{where}: ① 不得用 google.protobuf.Timestamp，时刻用 int64 Unix 毫秒")
            if path not in strict:
                continue
            if ftype == "string" and name.endswith("_at") and not matches(DATA_TIME_TEXT_EXCEPTIONS, path, message, name):
                violations.append(f"{where}: ② 以 _at 结尾的时刻不得是 string，改成 int64 Unix 毫秒")
            if FORBIDDEN_SUFFIX.search(name):
                violations.append(f"{where}: ④ 字段名不带单位后缀（_at_ms、_time_ms、_unix、_unix_nano），时刻写 int64 毫秒并保留原名")
            if (
                ftype in INT64_TYPES
                and TIME_NAME.search(name)
                and not DURATION_NAME.search(name)
                and not matches(DURATION_EXCEPTIONS, path, message, name)
                and not matches(SECONDS_WHITELIST, path, message, name)
                and SECONDS_IN_COMMENT.search(comment)
            ):
                violations.append(f"{where}: ③ 不在乙类白名单里的时刻是 Unix 毫秒，注释不得写“秒”“Unix 秒”“纳秒”（是数据坐标的秒就加进乙类白名单）")
    for item in violations:
        print(item)
    return 1 if violations else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
