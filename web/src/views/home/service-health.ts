import type { HealthOverview } from "@/api/monitor";
export function serviceHealth(overview?: HealthOverview, error = "") {
  const counts = overview?.summary?.components;
  if (error || !overview?.topology_known || !counts)
    return {
      available: false,
      healthy: undefined,
      total: 0,
      percent: undefined,
      tone: "neutral",
      note: error ? "健康数据读取失败" : "尚无完整健康观测",
      counts: undefined
    };
  const healthy = counts.healthy_count || 0;
  const attention = counts.attention_count || 0;
  const unknown = counts.unknown_count || 0;
  const unchecked = counts.unchecked_count || 0;
  const total = healthy + attention + unknown + unchecked;
  return {
    available: true,
    healthy,
    total,
    percent: total > 0 ? Math.round((healthy / total) * 100) : undefined,
    tone: attention > 0 ? "danger" : unknown > 0 || unchecked > 0 || total === 0 ? "neutral" : "ok",
    note: `正常 ${healthy} · 需关注 ${attention} · 未知 ${unknown} · 不探测 ${unchecked}`,
    counts
  };
}
