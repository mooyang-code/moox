import { toMillis } from "@/views/home/home-model";

/**
 * 市场时区的时间：加密货币 UTC，A 股 Asia/Shanghai（后端随数据集给出 IANA 时区名）；没有时区或时区无效时按 UTC。
 * K 线时间一律按市场时区显示并带时区标记，例如“18:54 UTC”“09:31 UTC+8”，不用浏览器本地时区。
 */

interface Parts {
  year: string;
  month: string;
  day: string;
  hour: string;
  minute: string;
  second: string;
}

const formatters = new Map<string, Intl.DateTimeFormat>();

function formatterFor(timeZone: string): Intl.DateTimeFormat {
  let formatter = formatters.get(timeZone);
  if (!formatter) {
    formatter = new Intl.DateTimeFormat("en-CA", {
      timeZone,
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
      hourCycle: "h23"
    });
    formatters.set(timeZone, formatter);
  }
  return formatter;
}

/** 规整时区名：空或无效时为 UTC。 */
export function marketZone(timeZone?: string): string {
  const zone = (timeZone || "").trim() || "UTC";
  try {
    formatterFor(zone);
    return zone;
  } catch {
    return "UTC";
  }
}

function partsOf(ms: number, timeZone: string): Parts {
  const out: Record<string, string> = {};
  for (const part of formatterFor(timeZone).formatToParts(new Date(ms))) out[part.type] = part.value;
  return out as unknown as Parts;
}

/** 时区标记：UTC、UTC+8。 */
export function zoneLabel(timeZone?: string, atMs: number = Date.now()): string {
  const zone = marketZone(timeZone);
  if (zone === "UTC" || zone === "Etc/UTC") return "UTC";
  const name = new Intl.DateTimeFormat("en-US", { timeZone: zone, timeZoneName: "shortOffset" })
    .formatToParts(new Date(atMs))
    .find(part => part.type === "timeZoneName")?.value;
  return !name || name === "GMT" ? "UTC" : name.replace("GMT", "UTC");
}

/** 市场时区的日期 YYYY-MM-DD。 */
export function marketDay(ms: number, timeZone?: string): string {
  const parts = partsOf(ms, marketZone(timeZone));
  return `${parts.year}-${parts.month}-${parts.day}`;
}

/**
 * 带时区标记的时间：与 nowMs 同一天（按市场时区）只写时分，否则写月-日 时分；seconds 为 true 时精确到秒。
 * 没有时间时为“—”。
 */
export function marketTime(
  value: string | number | null | undefined,
  timeZone: string | undefined,
  nowMs: number,
  options: { seconds?: boolean } = {}
): string {
  const at = typeof value === "number" ? value : toMillis(value);
  if (at === null) return "—";
  const zone = marketZone(timeZone);
  const parts = partsOf(at, zone);
  const clock = options.seconds ? `${parts.hour}:${parts.minute}:${parts.second}` : `${parts.hour}:${parts.minute}`;
  const sameDay = marketDay(at, zone) === marketDay(nowMs, zone);
  return `${sameDay ? clock : `${parts.month}-${parts.day} ${clock}`} ${zoneLabel(zone, at)}`;
}

/** 坐标轴用的短时间（不带时区标记，时区写在坐标轴标题上）。 */
export function marketClock(ms: number, timeZone?: string): string {
  const parts = partsOf(ms, marketZone(timeZone));
  return `${parts.hour}:${parts.minute}`;
}
