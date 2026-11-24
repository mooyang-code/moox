import type { StrategyTaskRequirement } from "@/api/strategy-types";

/** 策略涉及的市场，顺序固定：现货、永续、股票。 */
export const MARKET_ORDER = ["spot", "swap", "equity"] as const;

const marketLabels: Record<string, string> = {
  spot: "现货",
  swap: "永续",
  equity: "股票"
};

export function marketLabel(market: string): string {
  return marketLabels[market] || market || "-";
}

/** 附加来源的任务键：来源名:市场（市场与主帧的任务键一一对应）。 */
export function sourceTaskKey(source: string, market: string): string {
  return `${source}:${market}`;
}

/** 拆开任务键：附加来源的键是 来源名:市场，其余键的来源为空。 */
export function splitTaskKey(key: string): { source: string; market: string } {
  const at = key.indexOf(":");
  return at < 0 ? { source: "", market: key } : { source: key.slice(0, at), market: key.slice(at + 1) };
}

/** 任务键的展示文本：市场写市场名，附加来源写成 来源 daily（现货）。 */
export function taskKeyLabel(key: string): string {
  const { source, market } = splitTaskKey(key);
  return source ? `来源 ${source}（${marketLabel(market)}）` : marketLabel(market);
}

const sourceNamePattern = /^[a-z][a-z0-9_]{0,31}$/;

/**
 * 要绑定的任务键：后端已确定主帧市场时就是它给的键；没确定（股票策略不写 market）时按用户选的市场，
 * 主帧一个任务，附加来源各一个（来源名:市场）。还没读到要求或没选市场时为空。
 */
export function requirementTaskKeys(requirement: StrategyTaskRequirement | null | undefined, singleMarket: string): string[] {
  if (!requirement) return [];
  if (requirement.marketDeclared) return requirement.taskKeys;
  if (!singleMarket) return [];
  return [singleMarket, ...requirement.sources.map(source => sourceTaskKey(source.name, singleMarket))];
}

/** 参考市场（只用于年龄判定）：要求里给出、并且在任务键里的。 */
export function requirementReferenceMarkets(
  requirement: StrategyTaskRequirement | null | undefined,
  keys: readonly string[]
): string[] {
  return (requirement?.referenceMarkets ?? []).filter(market => keys.includes(market));
}

/** 主帧市场：任务键里不带来源、也不是参考市场的键，顺序同任务键。账户筛选、现算个数、资金费列都按它判断。 */
export function mainMarkets(requirement: StrategyTaskRequirement | null | undefined, singleMarket: string): string[] {
  const keys = requirementTaskKeys(requirement, singleMarket);
  const references = requirementReferenceMarkets(requirement, keys);
  return keys.filter(key => !splitTaskKey(key).source && !references.includes(key));
}

/** 实例绑定的计算任务，按市场先后排列，附加来源的任务（来源名:市场）按键排在最后。 */
export function instanceTasks(tasks: Record<string, string> | undefined): { market: string; taskId: string }[] {
  const plain = MARKET_ORDER.filter(market => tasks?.[market]).map(market => ({ market, taskId: tasks![market] }));
  const sources = Object.keys(tasks ?? {})
    .filter(key => splitTaskKey(key).source)
    .sort()
    .map(key => ({ market: key, taskId: tasks![key] }));
  return [...plain, ...sources];
}

/** 计算任务的展示文本：单任务只写 ID，多任务写成 现货 xxx、永续 yyy、来源 daily（现货） zzz。 */
export function tasksText(tasks: Record<string, string> | undefined): string {
  const items = instanceTasks(tasks);
  if (items.length <= 1) return items[0]?.taskId ?? "";
  return items.map(item => `${taskKeyLabel(item.market)} ${item.taskId}`).join("、");
}

/** 计算任务写进 URL 的格式：spot=ftask_a,swap=ftask_b（按现货、永续、股票先后）；没有任务返回空串。 */
export function formatFactorTasks(tasks: Record<string, string> | undefined): string {
  return instanceTasks(tasks)
    .map(item => `${item.market}=${item.taskId}`)
    .join(",");
}

/** 解析 URL 里的计算任务；只认现货、永续、股票三个市场，以及附加来源的 来源名:市场，其余与格式不对的项忽略。 */
export function parseFactorTasks(raw: string): Record<string, string> {
  const tasks: Record<string, string> = {};
  for (const part of raw.split(",")) {
    const [key, taskId] = part.split("=").map(item => item.trim());
    const { source, market } = splitTaskKey(key ?? "");
    const knownMarket = (MARKET_ORDER as readonly string[]).includes(market);
    if (taskId && knownMarket && (!key.includes(":") || sourceNamePattern.test(source))) tasks[key] = taskId;
  }
  return tasks;
}

/** 持仓键（标的|市场）的展示文本：写成 BTC-USDT（永续）；没有市场后缀的键原样返回。 */
export function positionLabel(key: string): string {
  const at = key.lastIndexOf("|");
  return at < 0 ? key : `${key.slice(0, at)}（${marketLabel(key.slice(at + 1))}）`;
}

/** 目标在表格里的市场标记（现货、永续、股票）；旧数据没有市场时为空。 */
export function targetKey(instrumentId: string, market: string): string {
  return `${instrumentId}|${market}`;
}
