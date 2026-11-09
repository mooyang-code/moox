import { parseDocument } from "yaml";

/** 策略涉及的市场，顺序固定：现货、永续、股票。 */
export const MARKET_ORDER = ["spot", "swap", "equity"] as const;

/** 统一数据集上的计算任务登记的键：一个任务同时覆盖现货与永续（数据集的 market_type 是 multi）。 */
export const UNIFIED_TASK_KEY = "multi";

/** 计算任务的键，顺序固定：三个市场，然后是统一数据集上的任务。 */
export const TASK_KEY_ORDER = [...MARKET_ORDER, UNIFIED_TASK_KEY] as const;

export const marketLabels: Record<string, string> = {
  spot: "现货",
  swap: "永续",
  equity: "股票",
  [UNIFIED_TASK_KEY]: "统一数据集"
};

export function marketLabel(market: string): string {
  return marketLabels[market] || market || "-";
}

export interface MarketRequirement {
  /** DSL 里写了 market。没写时只绑一个任务，市场由用户在创建时选定。 */
  declared: boolean;
  /** 需要绑定计算任务的市场（现货在前）。没写 market 时为空。 */
  markets: string[];
}

/**
 * 从 DSL 文本读出市场模式需要的计算任务：select 为 both 或 prefer 与 select 不同要现货、永续各一个，其余只要一个。
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

/** 附加来源与参考市场：sources 是 DSL 声明的附加来源（名字与周期，声明顺序），references 是 market.reference 写出的参考市场。 */
export interface SourceRequirement {
  sources: { name: string; bar: string }[];
  references: string[];
}

/** 从 DSL 文本读出附加来源与参考市场；解析失败或都没写时为空（后端校验写法）。 */
export function sourceRequirement(dslYaml: string): SourceRequirement {
  try {
    const document = parseDocument(dslYaml).toJS();
    const sources = document?.sources && typeof document.sources === "object" ? document.sources : {};
    const reference = Array.isArray(document?.market?.reference) ? document.market.reference : [];
    return {
      sources: Object.entries(sources).map(([name, value]: [string, any]) => ({ name, bar: String(value?.bar ?? "") })),
      references: reference.map((item: unknown) => String(item)).filter((item: string) => item === "spot" || item === "swap")
    };
  } catch {
    return { sources: [], references: [] };
  }
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

/** 策略池的成员引用；DSL 不是策略池（没有 strategy_pool）或无法解析时返回 null。 */
export function poolMembers(dslYaml: string): { id: string; strategyId: string }[] | null {
  try {
    const pool = parseDocument(dslYaml).toJS()?.strategy_pool;
    if (pool === undefined || pool === null) return null;
    const members = Array.isArray(pool?.members) ? pool.members : [];
    return members.map((member: any) => ({ id: String(member?.id ?? ""), strategyId: String(member?.strategy ?? "") }));
  } catch {
    return null;
  }
}

export interface PoolRequirement {
  /** 池要绑定计算任务的市场：各成员所需市场的并集（现货在前）；有问题时为空。 */
  markets: string[];
  /** 阻止创建的问题（中文）：成员定义读不到、没写 market、是策略池等。 */
  problems: string[];
}

/**
 * 策略池要绑定的计算任务：成员引用的策略定义各自按 marketRequirement 解析市场，取并集。
 * lookup 返回成员策略的 DSL 文本；null 表示确认不存在，undefined 表示还在读取。不是策略池时返回 null。
 */
export function poolRequirement(
  dslYaml: string,
  lookup: (strategyId: string) => string | null | undefined
): PoolRequirement | null {
  const members = poolMembers(dslYaml);
  if (!members) return null;
  if (!members.length) return { markets: [], problems: ["策略池没有写成员（strategy_pool.members）"] };
  const problems: string[] = [];
  const markets = new Set<string>();
  for (const member of members) {
    const label = `成员 ${member.id || "?"}`;
    if (!member.strategyId) {
      problems.push(`${label} 没有写 strategy`);
      continue;
    }
    const memberDsl = lookup(member.strategyId);
    if (memberDsl === undefined) {
      problems.push(`正在读取${label}引用的策略 ${member.strategyId}…`);
    } else if (memberDsl === null) {
      problems.push(`${label} 引用的策略 ${member.strategyId} 不存在（定义已删除，或读取失败）`);
    } else if (poolMembers(memberDsl)) {
      problems.push(`${label} 引用的策略 ${member.strategyId} 本身是策略池，策略池不能嵌套`);
    } else {
      const requirement = marketRequirement(memberDsl);
      if (!requirement.declared)
        problems.push(`${label} 引用的策略 ${member.strategyId} 没有显式写 market，池成员必须写 market（股票策略不能进池）`);
      else requirement.markets.forEach(market => markets.add(market));
    }
  }
  return { markets: problems.length ? [] : MARKET_ORDER.filter(market => markets.has(market)), problems };
}

/** 实例绑定的计算任务，按市场先后排列（统一数据集上的任务键是 multi，排在市场之后），附加来源的任务（来源名:市场）按键排在最后。 */
export function instanceTasks(tasks: Record<string, string> | undefined): { market: string; taskId: string }[] {
  const plain = TASK_KEY_ORDER.filter(market => tasks?.[market]).map(market => ({ market, taskId: tasks![market] }));
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

/** 解析 URL 里的计算任务；只认现货、永续、股票三个市场与统一数据集（multi），以及附加来源的 来源名:市场，其余与格式不对的项忽略。 */
export function parseFactorTasks(raw: string): Record<string, string> {
  const tasks: Record<string, string> = {};
  for (const part of raw.split(",")) {
    const [key, taskId] = part.split("=").map(item => item.trim());
    const { source, market } = splitTaskKey(key ?? "");
    const knownMarket = (TASK_KEY_ORDER as readonly string[]).includes(market);
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
