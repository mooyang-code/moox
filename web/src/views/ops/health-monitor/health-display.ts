export function statusLabel(status?: string) {
  switch (status) { case "healthy": case "ok": return "正常"; case "degraded": case "stale": return "需关注"; case "down": case "firing": return "异常"; default: return "未知"; }
}

export function statusColor(status?: string) {
  switch (status) { case "healthy": case "ok": return "green"; case "degraded": case "stale": return "orange"; case "down": case "firing": return "red"; default: return "gray"; }
}

export function formatCheckedAt(value?: string) { if (!value) return "暂无"; const date = new Date(value); return Number.isNaN(date.getTime()) ? "暂无" : date.toLocaleString("zh-CN", { hour12: false }); }

const conclusionTranslations: Array<[string, string]> = [
  ["reporter fresh", "监控上报正常"],
  ["reporter missing", "监控上报已中断"],
  ["producer stale", "数据生产端长时间未更新"],
  ["health not checked", "尚未完成健康检查"],
  ["health check failed", "健康检查失败"],
  ["health check degraded", "健康检查需要关注"],
  ["health check ok", "健康检查正常"],
  ["agent reachable", "主机连接正常"],
  ["agent unreachable", "主机无法连接"],
  ["balance sync fresh", "账户余额同步正常"],
  ["balance sync stale", "账户余额同步延迟"],
  ["balance sync failed 3 consecutive runs", "账户余额已连续三次同步失败"],
  ["run stale", "任务运行结果已过期"],
  ["success stale", "最近成功结果已过期"],
  ["inventory_stale", "资源清单已过期"],
  ["check failed", "检查失败"],
  ["normal", "正常"],
  ["unknown", "未知"]
];

export function displayConclusion(value?: string): string {
  const raw = (value || "").trim();
  if (!raw) return "";
  if (raw.includes(";")) return raw.split(";").map(part => displayConclusion(part)).filter(Boolean).join("；");
  const exact = conclusionTranslations.find(([source]) => source === raw);
  if (exact) return exact[1];
  if (raw.startsWith("balance difference ")) {
    const values = raw.slice("balance difference ".length).split(/\s+/);
    if (values.length === 3 && values[1] === "exceeds") return `账户余额差异超过阈值（当前值 ${values[0]}，阈值 ${values[2]}）`;
    return "账户余额差异超过阈值";
  }
  if (/^[\x00-\x7F]+$/.test(raw)) return "监控检查失败，请查看日志详情";
  return raw;
}
