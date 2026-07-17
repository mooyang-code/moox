export function formatPercent(value?: string | null): string {
  if (value === undefined || value === null || value === "") return "-";
  const number = Number(value);
  return Number.isFinite(number) ? `${(number * 100).toFixed(2)}%` : "-";
}

export function formatMetric(metric: { status?: string; value?: string | null }): string {
  if (metric.status === "insufficient_data") return "数据不足";
  if (metric.status === "stale") return "数据过期";
  return metric.value === undefined || metric.value === null || metric.value === "" ? "-" : metric.value;
}
