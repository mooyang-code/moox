/**
 * 时间显示和输入的统一工具。
 *
 * 规则（执行计划批次 2、拍板 7）：
 * - 跟空间有关的页面按当前空间登记的时区（crypto 为 UTC，A 股、港股为 UTC+8，美股为美东并随夏令时），
 *   和空间无关的页面用 UTC；
 * - 所有时间都带数字偏移后缀：UTC、UTC+8、UTC−4（负号是 U+2212），不用 CST、EST 这类缩写；
 * - 不显示毫秒、纳秒；时间输入按同一个时区解释墙钟。
 *
 * 只用浏览器自带的 Intl，不依赖浏览器本地时区。
 */

export type TimePrecision = "second" | "minute" | "date" | "clock";

interface Parts {
  year: string;
  month: string;
  day: string;
  hour: string;
  minute: string;
  second: string;
}

const MINUS = "−";
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

/** 规整时区名：空或不是合法的 IANA 名称时为 UTC。 */
export function resolveZone(timeZone?: string | null): string {
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

/** 这个时刻在该时区相对 UTC 的偏移（分钟，东正西负）。 */
export function zoneOffsetMinutes(ms: number, timeZone: string): number {
  const parts = partsOf(ms, timeZone);
  const asUtc = Date.UTC(+parts.year, +parts.month - 1, +parts.day, +parts.hour, +parts.minute, +parts.second);
  return Math.round((asUtc - Math.floor(ms / 1000) * 1000) / 60_000);
}

/** 时区后缀：零偏移写 UTC，其余写 UTC+8、UTC−4、UTC+5:30。按这个时刻的实际偏移算，夏令时自动变化。 */
export function zoneSuffix(ms: number, timeZone?: string | null): string {
  const offset = zoneOffsetMinutes(ms, resolveZone(timeZone));
  if (offset === 0) return "UTC";
  const sign = offset > 0 ? "+" : MINUS;
  const abs = Math.abs(offset);
  const hours = Math.floor(abs / 60);
  const minutes = abs % 60;
  return `UTC${sign}${hours}${minutes ? `:${String(minutes).padStart(2, "0")}` : ""}`;
}

/**
 * 把后端给的时间换成毫秒时刻：毫秒数字、纯数字字符串（毫秒）、RFC3339（含纳秒、Z 或数字偏移）。
 * 不带时区的 “YYYY-MM-DD HH:mm:ss” 按 UTC 理解。秒级时间由调用方先乘 1000，这里不猜单位。
 */
export function toInstantMs(value: string | number | Date | null | undefined): number | null {
  if (value === null || value === undefined || value === "") return null;
  if (value instanceof Date) return Number.isFinite(value.getTime()) ? value.getTime() : null;
  if (typeof value === "number") return Number.isFinite(value) && value > 0 ? value : null;
  const text = value.trim();
  if (/^\d+$/.test(text)) {
    const ms = Number(text);
    return ms > 0 ? ms : null;
  }
  // 小数秒只留到毫秒；没有时区的按 UTC
  let normalized = text.replace(" ", "T").replace(/(\.\d{3})\d+/, "$1");
  if (/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2}(\.\d+)?)?$/.test(normalized)) normalized += "Z";
  const parsed = Date.parse(normalized);
  return Number.isFinite(parsed) ? parsed : null;
}

/** 墙钟文本（不带后缀）：second 为 “YYYY-MM-DD HH:mm:ss”，minute 去掉秒，date 只有日期，clock 只有时分秒。 */
export function msToWallClock(ms: number, timeZone: string | null | undefined, precision: TimePrecision = "second"): string {
  const p = partsOf(ms, resolveZone(timeZone));
  const date = `${p.year}-${p.month}-${p.day}`;
  if (precision === "date") return date;
  if (precision === "clock") return `${p.hour}:${p.minute}:${p.second}`;
  if (precision === "minute") return `${date} ${p.hour}:${p.minute}`;
  return `${date} ${p.hour}:${p.minute}:${p.second}`;
}

/** 带时区后缀的时间；没有时间时为 “—”。 */
export function formatInstant(
  value: string | number | Date | null | undefined,
  timeZone: string | null | undefined,
  precision: TimePrecision = "second"
): string {
  const ms = toInstantMs(value);
  if (ms === null) return "—";
  return `${msToWallClock(ms, timeZone, precision)} ${zoneSuffix(ms, timeZone)}`;
}

/**
 * 把某个时区的墙钟（“YYYY-MM-DD”“YYYY-MM-DD HH:mm”“YYYY-MM-DD HH:mm:ss”）换成毫秒时刻。
 * 夏令时跳过的墙钟（美东 3 月的 02:30）顺延到切换后；重复的墙钟（11 月的 01:30）取较早的那个。
 */
export function wallClockToMs(text: string, timeZone?: string | null): number | null {
  const match = /^(\d{4})-(\d{2})-(\d{2})(?:[ T](\d{2}):(\d{2})(?::(\d{2}))?)?$/.exec(text.trim());
  if (!match) return null;
  const [, y, mo, d, h = "0", mi = "0", s = "0"] = match;
  const zone = resolveZone(timeZone);
  const guess = Date.UTC(+y, +mo - 1, +d, +h, +mi, +s);
  const wall = msToWallClock(guess, "UTC");
  // 切换点前后的偏移各算一次：正常时两个相同；重复时两个都对，取早的；跳过时都不对，按切换前的偏移顺延。
  const before = guess - zoneOffsetMinutes(guess - 86_400_000, zone) * 60_000;
  const after = guess - zoneOffsetMinutes(guess + 86_400_000, zone) * 60_000;
  const valid = [before, after].filter(ms => msToWallClock(ms, zone) === wall).sort((a, b) => a - b);
  return valid[0] ?? before;
}

// 以下三个保留给运维监控按市场显示的时间（到达图、链路收线时间）。

/** 市场时区的日期 YYYY-MM-DD。 */
export function marketDay(ms: number, timeZone?: string): string {
  return msToWallClock(ms, timeZone, "date");
}

/**
 * 带时区后缀的短时间：与 nowMs 同一天（按该时区）只写时分，否则写月-日 时分；seconds 为 true 时精确到秒。
 * 没有时间时为 “—”。
 */
export function marketTime(
  value: string | number | null | undefined,
  timeZone: string | undefined,
  nowMs: number,
  options: { seconds?: boolean } = {}
): string {
  const at = toInstantMs(value);
  if (at === null) return "—";
  const zone = resolveZone(timeZone);
  const p = partsOf(at, zone);
  const clock = options.seconds ? `${p.hour}:${p.minute}:${p.second}` : `${p.hour}:${p.minute}`;
  const sameDay = marketDay(at, zone) === marketDay(nowMs, zone);
  return `${sameDay ? clock : `${p.month}-${p.day} ${clock}`} ${zoneSuffix(at, zone)}`;
}

/** 坐标轴用的短时间（不带后缀，时区写在坐标轴标题上）。 */
export function marketClock(ms: number, timeZone?: string): string {
  const p = partsOf(ms, resolveZone(timeZone));
  return `${p.hour}:${p.minute}`;
}
