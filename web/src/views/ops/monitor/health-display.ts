export function statusLabel(status?: string) {
  switch (status) {
    case "healthy":
      return "正常";
    case "degraded":
      return "需关注";
    case "down":
      return "异常";
    case "disabled":
      return "已停用";
    case "unchecked":
      return "不探测";
    default:
      return "未知";
  }
}

export function statusColor(status?: string) {
  switch (status) {
    case "healthy":
      return "green";
    case "degraded":
      return "orange";
    case "down":
      return "red";
    default:
      return "gray";
  }
}

export function formatCheckedAt(value?: string) {
  if (!value) return "暂无";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "暂无" : date.toLocaleString("zh-CN", { hour12: false });
}

export function formatDuration(start?: string, end?: string) {
  const first = Date.parse(start || "");
  const last = Date.parse(end || "");
  if (!Number.isFinite(first) || !Number.isFinite(last)) return "暂无";
  const seconds = Math.max(0, Math.floor((last - first) / 1000));
  if (seconds < 60) return `${seconds} 秒`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)} 分钟`;
  return `${Math.floor(seconds / 3600)} 小时 ${Math.floor((seconds % 3600) / 60)} 分钟`;
}
