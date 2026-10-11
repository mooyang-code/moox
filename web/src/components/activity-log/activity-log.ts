/** 活动日志的展示辅助。 */

export function activityLevelColor(level?: string) {
  switch (level) {
    case "error":
      return "red";
    case "warning":
      return "orangered";
    default:
      return "green";
  }
}

export function activityTimeText(value?: string) {
  if (!value) return "-";
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return value;
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${parsed.getFullYear()}-${pad(parsed.getMonth() + 1)}-${pad(parsed.getDate())} ${pad(parsed.getHours())}:${pad(parsed.getMinutes())}:${pad(parsed.getSeconds())}`;
}

/** 详情是 JSON 文本：能解析就缩进展示，否则原样展示。 */
export function activityDetailText(detail?: string) {
  if (!detail) return "";
  try {
    return JSON.stringify(JSON.parse(detail), null, 2);
  } catch {
    return detail;
  }
}
