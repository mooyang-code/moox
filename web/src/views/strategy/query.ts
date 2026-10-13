/** 路由 query 的单个原始值：字符串、空值或它们的数组。 */
type RawValue = string | null | undefined | (string | null)[];
type QueryLike = Record<string, unknown>;

/** 取 query 的第一个值并去掉首尾空白；缺省或数组为空时为空串。 */
export function queryString(raw: RawValue): string {
  const value = Array.isArray(raw) ? raw[0] : raw;
  return typeof value === "string" ? value.trim() : "";
}

/** 页码：正整数，非法、小于 1 或缺省时为 1。 */
export function queryPage(raw: RawValue): number {
  const value = Number(queryString(raw));
  return Number.isInteger(value) && value >= 1 ? value : 1;
}

/** 枚举值：不在允许范围内（含缺省）时取 fallback。 */
export function queryEnum<T extends string>(raw: RawValue, allowed: readonly T[], fallback: T): T {
  const value = queryString(raw);
  return (allowed as readonly string[]).includes(value) ? (value as T) : fallback;
}

/** 构造 query：丢弃空值与默认值，数字与布尔转为字符串；键按字典序，便于比较。 */
export function buildQuery(entries: Record<string, string | number | boolean | undefined | null>, defaults: Record<string, string | number | boolean> = {}): Record<string, string> {
  const query: Record<string, string> = {};
  for (const key of Object.keys(entries).sort()) {
    const value = entries[key];
    if (value === undefined || value === null || value === "") continue;
    if (key in defaults && String(defaults[key]) === String(value)) continue;
    query[key] = String(value);
  }
  return query;
}

/** 两份 query 的语义是否相同：只比较各自第一个值，忽略空值，避免为同一状态重复导航。 */
export function sameQuery(a: QueryLike, b: QueryLike): boolean {
  const normalize = (query: QueryLike) =>
    Object.keys(query)
      .sort()
      .map(key => [key, queryString(query[key] as RawValue)] as const)
      .filter(([, value]) => value !== "");
  return JSON.stringify(normalize(a)) === JSON.stringify(normalize(b));
}
