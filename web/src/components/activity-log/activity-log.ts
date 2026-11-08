/** 活动日志的展示辅助。 */
import { formatInstant } from "@/utils/time-zone";

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

/** 操作记录的时间：按调用方传入的时区带后缀显示（当前空间的记录用空间时区，系统事件用 UTC）。 */
export function activityTimeText(value: string | undefined, zone: string) {
  return formatInstant(value, zone);
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
