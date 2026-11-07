/** 平仓动作状态的说明文字 */
export function flattenStatusText(status: string): string {
  switch (status) {
    case "RUNNING":
      return "平仓执行中…";
    case "COMPLETED":
      return "平仓完成";
    case "PARTIAL":
      return "部分完成：还有仓位没有平掉，原因见下方";
    case "FAILED":
      return "平仓失败";
    default:
      return status || "-";
  }
}
