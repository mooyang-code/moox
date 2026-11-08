import { operatorActionTypeLabels, type OperatorAction, type OperatorActionType } from "@/api/trade";

export type ActionTone = "ok" | "warn" | "bad" | "run";

export const operatorActionTypeOptions = (Object.keys(operatorActionTypeLabels) as OperatorActionType[]).map(value => ({
  value,
  label: operatorActionTypeLabels[value]
}));

/** 类型的中文名；后端新增了类型而前端还没跟上时，原样显示。 */
export function operatorActionTypeText(type: string): string {
  return operatorActionTypeLabels[type as OperatorActionType] ?? (type || "未知");
}

export function operatorActionTone(status: string): ActionTone {
  switch (status) {
    case "COMPLETED":
      return "ok";
    case "PARTIAL":
      return "warn";
    case "FAILED":
      return "bad";
    default:
      return "run";
  }
}

/** result_json 不原样展示，只认顶层 unblocked：下架清仓因标的被解除禁止而提前结束。解析不了就当没有提示。 */
export function operatorActionHint(action: OperatorAction): string {
  if (!action.result_json) return "";
  try {
    const result: unknown = JSON.parse(action.result_json);
    if (result && typeof result === "object" && (result as { unblocked?: unknown }).unblocked === true) {
      return "标的已解除禁止，清仓提前结束";
    }
  } catch {
    // 结果不是合法 JSON：不影响列表，只是没有提示
  }
  return "";
}
