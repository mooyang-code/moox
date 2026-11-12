import type { LogRecord } from "@/api/monitor";

/** 级别的中文：关键事件按 info 级写出，单独叫“事件”。 */
export function levelLabel(record: LogRecord): string {
  if (record.event && levelTone(record) !== "error") return "事件";
  const names: Record<string, string> = {
    error: "错误",
    fatal: "致命",
    panic: "致命",
    dpanic: "致命",
    warn: "警告",
    info: "信息",
    debug: "调试"
  };
  return names[record.level ?? ""] ?? record.level ?? "--";
}

/** 级别的颜色：错误和致命标红，其余不强调。 */
export function levelTone(record: LogRecord): "error" | "event" {
  return ["error", "fatal", "panic", "dpanic"].includes(record.level ?? "") ? "error" : "event";
}

/** 正文的第一行（列表里只显示一行，完整内容在详情里）。 */
export function firstLine(message?: string): string {
  return (message ?? "").split("\n", 1)[0];
}

/** 被合并掉的条数；接口里的 int32 可能是数字或字符串。 */
export function mergedCount(record: LogRecord): number {
  const value = Number(record.merged_count ?? 0);
  return Number.isFinite(value) && value > 0 ? value : 0;
}

/** 字段按键名排序，便于对比两条日志。 */
export function fieldEntries(record: LogRecord): Array<[string, string]> {
  return Object.entries(record.fields ?? {}).sort(([a], [b]) => a.localeCompare(b));
}
