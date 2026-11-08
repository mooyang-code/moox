const DAY = 86_400_000;

export const recordRanges = [
  { key: "1d", label: "近 24 小时", ms: DAY },
  { key: "7d", label: "近 7 天", ms: 7 * DAY },
  { key: "30d", label: "近 30 天", ms: 30 * DAY },
  { key: "all", label: "全部", ms: 0 }
] as const;

export type RecordRange = (typeof recordRanges)[number]["key"];

/** 地址里的时间范围；没有或不认识时用默认的近 7 天。 */
export function rangeFromQuery(value: unknown): RecordRange {
  return recordRanges.find(item => item.key === value)?.key ?? "7d";
}

/** 订单、成交按时间范围分页读取；“全部”不带时间条件。 */
export function rangeParams(range: RecordRange, now = Date.now()): { start_time?: string; end_time?: string } {
  const ms = recordRanges.find(item => item.key === range)?.ms ?? 0;
  return ms ? { start_time: String(now - ms), end_time: String(now) } : {};
}
