import type { StrategyResult } from "@/api/strategy-types";

// 策略池详情的纯函数：从每期结果的 summary_json 里取出 pool 段，整理成按时间正序的期列表，再构造净值、比例、择时序列。
// 所有比例与权重在后端是定点小数字符串，这里只转成数字用于画图与展示，不做任何会改变含义的运算。

/** 成员颜色色板（Arco 的 CSS 变量），按成员顺序固定分配，超过色板长度时循环。 */
export const POOL_PALETTE: readonly string[] = [
  "rgb(var(--arcoblue-6))",
  "rgb(var(--orangered-6))",
  "rgb(var(--green-6))",
  "rgb(var(--purple-6))",
  "rgb(var(--gold-6))",
  "rgb(var(--cyan-6))",
  "rgb(var(--magenta-6))",
  "rgb(var(--lime-6))",
  "rgb(var(--pinkpurple-6))",
  "rgb(var(--orange-6))",
  "rgb(var(--red-6))",
  "rgb(var(--blue-4))"
];

/** 第 index 个成员的颜色（从 0 起；负数与非整数按 0 处理）。 */
export function memberColor(index: number): string {
  const safe = Number.isInteger(index) && index >= 0 ? index : 0;
  return POOL_PALETTE[safe % POOL_PALETTE.length];
}

/** 定点小数字符串（或数字）转数字；空值与无法解析的值返回 null。四舍五入到 10 位小数，消除 0.1 + 0.2 这类浮点尾巴。 */
export function poolNumber(value: unknown): number | null {
  if (value === null || value === undefined) return null;
  if (typeof value !== "number" && typeof value !== "string") return null;
  if (typeof value === "string" && value.trim() === "") return null;
  const number = Number(value);
  if (!Number.isFinite(number)) return null;
  return Math.round(number * 1e10) / 1e10;
}

/** 某个成员对某个（标的|市场）的贡献权重。 */
export interface PoolTarget {
  /** 原始键：标的|市场。 */
  key: string;
  instrumentId: string;
  market: string;
  weight: number;
}

export interface PoolMemberDetail {
  id: string;
  status: "ok" | "skipped";
  reason: string;
  detail: string;
  /** 成员模拟账本本期末净值；缺失时为 null。 */
  equity: number | null;
  bankrupt: boolean;
  base: number | null;
  /** 择时：1 持有，0 空仓；没有择时时为 null。 */
  timing: 0 | 1 | null;
  ratioBefore: number | null;
  /** 步长限制之后、实际用于合成的比例；缺失按 0 计。 */
  ratio: number;
  rank: number | null;
  gross: number | null;
  net: number | null;
  /** 按贡献权重绝对值从大到小排列。 */
  targets: PoolTarget[];
}

export interface PoolPeriod {
  resultId: string;
  sessionId: string;
  /** K 线结束时间（原文）。 */
  time: string;
  timeMs: number;
  /** 这一期结果本身的状态；池整体跳过时成员明细仍然存在。 */
  resultStatus: string;
  method: string;
  /** 本期叠加了择时。 */
  timing: boolean;
  members: PoolMemberDetail[];
}

function splitTargetKey(key: string): { instrumentId: string; market: string } {
  const at = key.lastIndexOf("|");
  return at < 0 ? { instrumentId: key, market: "" } : { instrumentId: key.slice(0, at), market: key.slice(at + 1) };
}

function parseMember(raw: any): PoolMemberDetail | null {
  if (!raw || typeof raw !== "object" || typeof raw.id !== "string" || !raw.id) return null;
  const timing = raw.timing === "1" || raw.timing === 1 ? 1 : raw.timing === "0" || raw.timing === 0 ? 0 : null;
  const targets: PoolTarget[] = [];
  if (raw.targets && typeof raw.targets === "object" && !Array.isArray(raw.targets)) {
    for (const [key, value] of Object.entries(raw.targets)) {
      const weight = poolNumber(value);
      if (weight !== null) targets.push({ key, ...splitTargetKey(key), weight });
    }
    targets.sort((a, b) => Math.abs(b.weight) - Math.abs(a.weight) || a.key.localeCompare(b.key));
  }
  return {
    id: raw.id,
    status: raw.status === "skipped" ? "skipped" : "ok",
    reason: typeof raw.reason === "string" ? raw.reason : "",
    detail: typeof raw.detail === "string" ? raw.detail : "",
    equity: poolNumber(raw.equity),
    bankrupt: raw.bankrupt === true,
    base: poolNumber(raw.base),
    timing,
    ratioBefore: poolNumber(raw.ratio_before),
    ratio: poolNumber(raw.ratio) ?? 0,
    rank: poolNumber(raw.rank),
    gross: poolNumber(raw.gross),
    net: poolNumber(raw.net),
    targets
  };
}

/** 解析一期结果里的策略池明细；没有 pool、JSON 损坏、时间无效或没有任何成员时返回 null。同一期内重复的成员 id 只取第一个。 */
export function parsePoolPeriod(
  result: Pick<StrategyResult, "result_id" | "session_id" | "bar_end_time" | "status" | "summary_json">
): PoolPeriod | null {
  if (!result.summary_json) return null;
  let summary: any;
  try {
    summary = JSON.parse(result.summary_json);
  } catch {
    return null;
  }
  const pool = summary?.pool;
  if (!pool || typeof pool !== "object" || !Array.isArray(pool.members)) return null;
  const timeMs = Date.parse(result.bar_end_time);
  if (!Number.isFinite(timeMs)) return null;
  const seen = new Set<string>();
  const members: PoolMemberDetail[] = [];
  for (const raw of pool.members) {
    const member = parseMember(raw);
    if (!member || seen.has(member.id)) continue;
    seen.add(member.id);
    members.push(member);
  }
  if (!members.length) return null;
  return {
    resultId: result.result_id,
    sessionId: result.session_id,
    time: result.bar_end_time,
    timeMs,
    resultStatus: result.status,
    method: typeof pool.method === "string" ? pool.method : "",
    timing: pool.timing === true,
    members
  };
}

/** 从已加载的结果里取出所有带策略池明细的期，按时间从旧到新排列（输入顺序不限；同一 result_id 只取一次）。 */
export function buildPoolPeriods(
  results: Pick<StrategyResult, "result_id" | "session_id" | "bar_end_time" | "status" | "summary_json">[]
): PoolPeriod[] {
  const seen = new Set<string>();
  const periods: PoolPeriod[] = [];
  for (const result of results) {
    if (seen.has(result.result_id)) continue;
    const period = parsePoolPeriod(result);
    if (!period) continue;
    seen.add(result.result_id);
    periods.push(period);
  }
  return periods.sort((a, b) => a.timeMs - b.timeMs);
}

/** 成员 id 列表：按首次出现的顺序（成员顺序在每期里一致，个别期缺成员时也不会打乱颜色）。 */
export function poolMemberIds(periods: PoolPeriod[]): string[] {
  const ids: string[] = [];
  const seen = new Set<string>();
  for (const period of periods) {
    for (const member of period.members) {
      if (seen.has(member.id)) continue;
      seen.add(member.id);
      ids.push(member.id);
    }
  }
  return ids;
}

/** 曾经爆仓的成员 id。 */
export function bankruptMemberIds(periods: PoolPeriod[]): Set<string> {
  const ids = new Set<string>();
  for (const period of periods) for (const member of period.members) if (member.bankrupt) ids.add(member.id);
  return ids;
}

export interface MemberSeries<T> {
  id: string;
  /** 与期列表等长；这一期没有该成员时为 null。 */
  values: (T | null)[];
}

function series<T>(periods: PoolPeriod[], ids: string[], pick: (member: PoolMemberDetail) => T | null): MemberSeries<T>[] {
  return ids.map(id => ({
    id,
    values: periods.map(period => {
      const member = period.members.find(item => item.id === id);
      return member ? pick(member) : null;
    })
  }));
}

/** 成员净值序列。 */
export function equitySeries(periods: PoolPeriod[], ids = poolMemberIds(periods)): MemberSeries<number>[] {
  return series(periods, ids, member => member.equity);
}

/** 成员实际比例序列（步长限制之后）；这一期没有该成员时按 0 计，便于堆叠。 */
export function ratioSeries(periods: PoolPeriod[], ids = poolMemberIds(periods)): MemberSeries<number>[] {
  return series(periods, ids, member => member.ratio).map(item => ({
    id: item.id,
    values: item.values.map(value => value ?? 0)
  }));
}

/** 成员择时序列：1 持有、0 空仓、null 没有择时。 */
export function timingSeries(periods: PoolPeriod[], ids = poolMemberIds(periods)): MemberSeries<0 | 1>[] {
  return series(periods, ids, member => member.timing);
}

/** 每期比例总和（堆叠面积图每一列的高度）。 */
export function ratioTotals(periods: PoolPeriod[]): number[] {
  return periods.map(period => Math.round(period.members.reduce((sum, member) => sum + member.ratio, 0) * 1e10) / 1e10);
}

/** 堆叠面积图 Y 轴上限：各期比例总和的最大值，至少 1。 */
export function ratioAxisMax(periods: PoolPeriod[]): number {
  return Math.max(1, ...ratioTotals(periods));
}

/** 相邻两期会话不同的位置（该索引的期与前一期不属于同一会话）；曲线在这里断开，不把不同会话的账本连成一条线。 */
export function sessionBreaks(periods: PoolPeriod[]): Set<number> {
  const breaks = new Set<number>();
  for (let index = 1; index < periods.length; index += 1) {
    if (periods[index].sessionId !== periods[index - 1].sessionId) breaks.add(index);
  }
  return breaks;
}

/** 把一条序列切成连续的线段（每段是期索引列表）：遇到缺值或会话断点就另起一段。 */
export function lineSegments(values: (number | null)[], breaks: Set<number> = new Set()): number[][] {
  const segments: number[][] = [];
  let current: number[] = [];
  values.forEach((value, index) => {
    if (value === null) {
      if (current.length) segments.push(current);
      current = [];
      return;
    }
    if (breaks.has(index) && current.length) {
      segments.push(current);
      current = [];
    }
    current.push(index);
  });
  if (current.length) segments.push(current);
  return segments;
}

/** 净值曲线 Y 轴范围：包含基准 1 与所有净值，上下各留 5% 边距；全部相同时给一个对称的小区间。 */
export function equityRange(all: MemberSeries<number>[]): { min: number; max: number } {
  const values = all.flatMap(item => item.values.filter((value): value is number => value !== null));
  const low = Math.min(1, ...values);
  const high = Math.max(1, ...values);
  const pad = high > low ? (high - low) * 0.05 : 0.05;
  return { min: low - pad, max: high + pad };
}
