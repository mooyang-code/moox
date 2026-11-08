/** 活动日志的展示辅助。 */
import { formatInstant } from "@/utils/time-zone";

/** 级别的文字颜色：错误红、警告橙，普通信息不上色（不用绿色）。 */
export function activityLevelColor(level?: string) {
  switch (level) {
    case "error":
      return "rgb(var(--danger-6))";
    case "warning":
      return "rgb(var(--warning-6))";
    default:
      return "var(--color-text-3)";
  }
}

export function activityLevelText(level?: string) {
  return ({ error: "错误", warning: "警告", info: "信息" } as Record<string, string>)[level || ""] || level || "-";
}

/** 操作记录的类别：首页和操作记录页共用的中文名；没有登记的原样显示。 */
export const ACTIVITY_CATEGORY_LABELS: Record<string, string> = {
  "collector.fetch_plan_applied": "采集计划",
  "collector.subject_change": "标的变更"
};

export function activityCategoryLabel(category?: string) {
  return ACTIVITY_CATEGORY_LABELS[category || ""] || category || "-";
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
