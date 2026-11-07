import { parseDocument } from "yaml";

/** 策略涉及的市场，顺序固定：现货、永续、股票。 */
export const MARKET_ORDER = ["spot", "swap", "equity"] as const;

export const marketLabels: Record<string, string> = { spot: "现货", swap: "永续", equity: "股票" };

export function marketLabel(market: string): string {
  return marketLabels[market] || market || "-";
}

export interface MarketRequirement {
  /** DSL 里写了 market。没写时只绑一个任务，市场由用户在创建时选定。 */
  declared: boolean;
  /** 需要绑定因子任务的市场（现货在前）。没写 market 时为空。 */
  markets: string[];
}

/**
 * 从 DSL 文本读出市场模式需要的因子任务：select 为 both 或 prefer 与 select 不同要现货、永续各一个，其余只要一个。
 * 解析失败或没写 market 时 declared 为 false（后端校验保存时的写法是否合法）。
 */
export function marketRequirement(dslYaml: string): MarketRequirement {
  try {
    const market = parseDocument(dslYaml).toJS()?.market;
    if (!market || typeof market !== "object") return { declared: false, markets: [] };
    const select = String(market.select ?? "");
    const prefer = String(market.prefer ?? select);
    if (select === "both" || (select && prefer && select !== prefer)) return { declared: true, markets: ["spot", "swap"] };
    if (select === "spot" || select === "swap") return { declared: true, markets: [select] };
  } catch {
    // 语法错误由后端校验报告
  }
  return { declared: false, markets: [] };
}

/** 实例绑定的因子任务，按市场先后排列。 */
export function instanceTasks(tasks: Record<string, string> | undefined): { market: string; taskId: string }[] {
  return MARKET_ORDER.filter(market => tasks?.[market]).map(market => ({ market, taskId: tasks![market] }));
}

/** 只绑一个任务的实例的因子任务 ID（回放等只读一个 View 的地方使用）；绑了多个返回空串。 */
export function singleTaskId(tasks: Record<string, string> | undefined): string {
  const items = instanceTasks(tasks);
  return items.length === 1 ? items[0].taskId : "";
}

/** 因子任务的展示文本：单任务只写 ID，多任务写成 现货 xxx、永续 yyy。 */
export function tasksText(tasks: Record<string, string> | undefined): string {
  const items = instanceTasks(tasks);
  if (items.length <= 1) return items[0]?.taskId ?? "";
  return items.map(item => `${marketLabel(item.market)} ${item.taskId}`).join("、");
}

/** 目标在表格里的市场标记（现货、永续、股票）；旧数据没有市场时为空。 */
export function targetKey(instrumentId: string, market: string): string {
  return `${instrumentId}|${market}`;
}
