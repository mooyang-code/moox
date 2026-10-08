// Dataset retention is resolved by Storage from moox.toml [storage_retention]
// and returned read-only as "<n>h" or "forever".

const SOURCE_LABELS: Record<string, string> = {
  default: "全局默认",
  space: "空间配置",
  record: "记录型不过期"
};

/** Formats a retention such as "168h" as "7 天" and "forever" as "永久". */
export function formatRetention(retention?: string): string {
  const value = retention?.trim() || "";
  if (!value) return "-";
  if (value === "forever") return "永久";
  const match = value.match(/^(\d+)h$/);
  if (!match) return value;
  const hours = Number(match[1]);
  return hours % 24 === 0 ? `${hours / 24} 天` : `${hours} 小时`;
}

/** Describes where a Dataset's retention comes from. */
export function retentionSourceLabel(source?: string): string {
  return SOURCE_LABELS[source?.trim() || ""] || "-";
}
