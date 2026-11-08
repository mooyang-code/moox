import { parseDocument } from "yaml";

/** 截面选股模板：假设关联的因子任务里有默认参数的 bias、bias_q、quote_volume_mean、quote_volume_mean_q 成员（列名就是成员键）；bias = close / MA（窗口默认 20）。 */
export const rankedTemplate = `name: binance_spot_momentum
bar: 1m

universe:
  min_age_bars: 240
  exclude: [BTC-USDT]

rules:
  - id: long_momentum
    name: 多头动量选币
    type: rank
    filter: "quote_volume_mean > 20000 && close > 0"
    score: "0.6 * rank(bias_q) + 0.4 * rank(quote_volume_mean_q)"
    select: {top: 5, buffer: 2}
    weight: {total: 0.8, method: equal, cap: 0.3}

  - id: btc_trend
    name: BTC 上穿均线
    type: signal
    pool: [BTC-USDT]
    entry: "bars[-1].bias <= 1 && bars[0].bias > 1"
    exit: "bars[0].bias < 1"
    weight: {total: 0.2}

portfolio:
  leverage: 1
  max_weight: 0.3
  min_universe: 20
  max_missing: 0.2
`;

/** 固定池择时模板：BTC、ETH 站上 20 期均线时持有。 */
export const signalTemplate = `name: majors_trend
bar: 1m

rules:
  - id: majors_trend
    name: 主流币均线择时
    type: signal
    pool: [BTC-USDT, ETH-USDT]
    entry: "bars[-1].bias <= 1 && bars[0].bias > 1"
    exit: "bars[0].bias < 1"
    weight: {total: 0.6}

portfolio:
  leverage: 1
`;

/**
 * 动态调参模板：策略不声明因子，规则直接引用关联因子任务的列名（成员键与列名规则见因子文档）；
 * 因子任务里要有可变成员 ma__var（加成员时选“可变”），factor_params 每期求出它下一期的参数
 * （default 是各标的取值的中位数，per_subject 可选，与统一值不同的标的进入覆盖；没有写出的参数取因子定义的默认值），
 * params("ma__var", "window") 读取这一行实际用的窗口。
 */
export const tunedTemplate = `name: tuned_ma
bar: 5m

factor_params:
  ma__var:
    window: "20"

rules:
  - id: above_ma
    name: 站上可变均线
    type: rank
    filter: 'ma__var > 0 && close > ma__var && params("ma__var", "window") >= 10'
    score: "rank(close / ma__var - 1)"
    select: {top: 5}
    weight: {total: 1, method: equal, cap: 0.3}

portfolio:
  leverage: 1
  min_universe: 20
  max_missing: 0.2
`;

/** 混合市场示例：market 写 {select: both, prefer: swap}，同一个币两边都有时优先永续下单；空头只走永续（现货不能做空）。 */
export const mixedMarketTemplate = `# 混合市场示例：从现货与永续两边选币，空头只走永续。
# 创建实例时绑定因子任务：现货、永续各一个（两个任务的列名要一致），或统一数据集上的一个任务；
# 混合市场不能用 tranche 规则与 factor_params。
name: mixed_market_momentum
bar: 1h

# 现货、永续两边选币；同一个币两边都有时优先在永续下单
market: {select: both, prefer: swap}

universe:
  min_age_bars: 240
  exclude: [BTC-USDT]

rules:
  - id: long_momentum
    name: 多头动量选币
    type: rank
    filter: "quote_volume_mean > 20000 && close > 0"
    score: "0.6 * rank(bias_q) + 0.4 * rank(quote_volume_mean_q)"
    select: {top: 5, buffer: 2}
    weight: {total: 0.6, method: equal, cap: 0.2}

  # 空头只走永续：选分数最低的几个币做空
  - id: short_weak
    name: 空头弱势做空
    type: rank
    side: short
    filter: "quote_volume_mean > 20000 && close > 0"
    score: "rank(bias_q)"
    select: {bottom: 3}
    weight: {total: 0.3, method: equal, cap: 0.15}

portfolio:
  leverage: 1
  max_weight: 0.3
  min_universe: 20
  max_missing: 0.2
`;

/**
 * 策略池示例：把已有的策略定义当作成员，按成员模拟净值轮动分配资金（allocation.method 也可以写 fixed，按 ratios 固定分配）。
 * 约束与占位说明写在模板开头的 YAML 注释里，编辑器里能直接看到。
 */
export const poolTemplate = `# 策略池示例：把已有的策略定义当作成员，按成员的模拟净值轮动分配资金。
# strategy 里的 strategy_trend_a、strategy_trend_b 是占位，请换成实际的策略定义 ID。
# 成员必须显式写 market，bar 要与池一致，不能是策略池，不能用 factor_params。
# 池本身不写 market、universe、factor_params，必须写 bar。
# 池要绑定的因子任务是各成员所需市场的并集：成员都是现货就绑一个现货任务，混有永续就现货、永续各绑一个。
# 启用后先在后台预热成员净值，预热完成前不能确认运行。
name: trend_rotation_pool
bar: 1h

strategy_pool:
  members:
    - {id: a, strategy: strategy_trend_a}
    - {id: b, strategy: strategy_trend_b}
  allocation:
    method: rotation
    rotation:
      # 因子作用在成员净值曲线上：return 区间收益，ma_ratio 净值相对均线，drawdown 区间最大回撤；可选 order: asc|desc
      factors:
        - {name: return, window: 24, weight: 1}
        - {name: ma_ratio, window: 72, weight: 1}
      top: 1
  # 成员净值在 24 期均线之上持有，否则空仓
  timing: {window: 24}
  # 每期每个成员的比例变化上限
  step: 0.2
  # 成员模拟账本的单边手续费（基点），可省略
  # fees: {spot_bps: 10, swap_bps: 5}

portfolio:
  leverage: 1
  max_weight: 0.1
`;

export interface DSLDiagnostic {
  message: string;
}

export interface DSLRulePreview {
  id: string;
  name: string;
  type: string;
  /** tranche 规则展开后的子策略数（参数表行数，没有参数表时为 1）；其他类型为 0。 */
  subStrategies: number;
}

/** 策略池的预览：成员、分配方式、择时与步长。 */
export interface DSLPoolPreview {
  memberIds: string[];
  /** fixed、rotation，或写错的原文；没写为空串。 */
  method: string;
  /** 轮动取前几名；fixed 或没写为 0。 */
  top: number;
  /** 择时窗口；没写择时为 0。 */
  timingWindow: number;
  /** 步长上限的原文；没写为空串。 */
  step: string;
}

export interface DSLPreview {
  name: string;
  bar: string;
  rules: DSLRulePreview[];
  universe: string;
  leverage: string;
  /** 市场模式的说明；没写 market 时是“按因子任务的市场”，策略池是成员市场的并集。 */
  market: string;
  /** 策略池策略才有；此时 rules 为空。 */
  pool?: DSLPoolPreview;
}

/** 本地只做 YAML 与结构预检；字段、表达式与列的完整校验以服务端 ValidateStrategy 为准。 */
export function parseDSL(source: string): { preview: DSLPreview | null; diagnostics: DSLDiagnostic[] } {
  const document = parseDocument(source, { uniqueKeys: true });
  const diagnostics: DSLDiagnostic[] = [...document.errors, ...document.warnings].map(item => ({ message: item.message }));
  if (document.errors.length > 0) return { preview: null, diagnostics };
  let value: Record<string, any> | null;
  try {
    value = document.toJS() as Record<string, any> | null;
  } catch (error) {
    // 例如未解析的别名：document.errors 里没有，但转换成对象时会抛异常。
    return { preview: null, diagnostics: [{ message: error instanceof Error ? error.message : "YAML 无法解析" }] };
  }
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return { preview: null, diagnostics: [{ message: "DSL 必须是 YAML 对象" }] };
  }
  if (typeof value.name !== "string" || !value.name.trim()) diagnostics.push({ message: "缺少 name" });
  const rules: DSLRulePreview[] = [];
  const hasPool = value.strategy_pool !== undefined && value.strategy_pool !== null;
  let pool: DSLPoolPreview | undefined;
  if (hasPool) {
    pool = inspectPool(value, diagnostics);
  } else if (!Array.isArray(value.rules) || value.rules.length === 0) {
    diagnostics.push({
      message: `rules 必须是非空列表，每条规则包含 id 与 type${value.rules === undefined ? "（策略池请写 strategy_pool）" : ""}`
    });
  } else {
    value.rules.forEach((rule: any, index: number) => {
      const id = typeof rule?.id === "string" ? rule.id : "";
      const type = typeof rule?.type === "string" ? rule.type : "";
      if (!id) diagnostics.push({ message: `rules[${index}] 缺少 id` });
      if (type !== "rank" && type !== "signal" && type !== "tranche")
        diagnostics.push({ message: `rules[${index}] 的 type 只能是 rank、signal 或 tranche` });
      const rows = rule?.params?.rows;
      const subStrategies = type === "tranche" ? (Array.isArray(rows) ? rows.length : 1) : 0;
      rules.push({ id: id || `#${index + 1}`, name: typeof rule?.name === "string" ? rule.name : "", type, subStrategies });
    });
    if (rules.some(rule => rule.type === "tranche") && rules.some(rule => rule.type !== "tranche"))
      diagnostics.push({ message: "含 tranche 规则的策略只能全部是 tranche 规则" });
  }
  const universe = value.universe && typeof value.universe === "object" ? describeUniverse(value.universe) : "全部标的";
  return {
    preview: {
      name: typeof value.name === "string" ? value.name : "未命名策略",
      bar: value.bar === undefined ? "由 View 决定" : String(value.bar),
      rules,
      universe,
      leverage: value.portfolio?.leverage === undefined ? "1" : String(value.portfolio.leverage),
      // 策略池不写 market：池涉及的市场是各成员所需市场的并集，选币和下单按成员自己的 market
      market: hasPool ? "成员市场的并集（各成员按自己的 market）" : describeMarket(value.market),
      ...(pool ? { pool } : {})
    },
    diagnostics
  };
}

const marketSelectText: Record<string, string> = { spot: "现货", swap: "永续", both: "现货与永续" };

/** market 的说明：没写时按因子任务的市场；写了就给出选币范围与同一个币两边都有时的下单市场。 */
function describeMarket(market: unknown): string {
  if (!market || typeof market !== "object" || Array.isArray(market)) return "按因子任务的市场";
  const { select, prefer } = market as Record<string, unknown>;
  const text = (value: unknown) => marketSelectText[String(value)] ?? String(value);
  const parts = [`选币范围 ${select === undefined ? "?" : text(select)}`];
  if (prefer !== undefined && prefer !== select) parts.push(`优先在${text(prefer)}下单`);
  return parts.join("，");
}

const rotationFactorNames = ["return", "ma_ratio", "drawdown"];

/** 策略池的本地结构预检（与 rules 互斥、必须写 bar、不写 market 等）；成员是否存在、市场与周期是否匹配由服务端校验。 */
function inspectPool(value: Record<string, any>, diagnostics: DSLDiagnostic[]): DSLPoolPreview | undefined {
  const report = (message: string) => diagnostics.push({ message });
  if (value.rules !== undefined) report("rules 与 strategy_pool 只能写一个：策略池的逻辑都在成员策略里");
  if (value.market !== undefined) report("策略池不写 market：成员各自显式写 market，池要绑定的因子任务是成员所需市场的并集");
  if (value.bar === undefined) report("策略池必须写 bar：成员的周期要与池一致");
  if (value.universe !== undefined) report("策略池不写 universe：标的范围由各成员自己的 universe 决定");
  if (value.factor_params !== undefined) report("策略池不支持 factor_params：多个成员可能给同一个可变成员写参数，会冲突");
  const pool = value.strategy_pool;
  if (typeof pool !== "object" || Array.isArray(pool)) {
    report("strategy_pool 必须是对象，包含 members 与 allocation");
    return undefined;
  }
  const memberIds: string[] = [];
  if (!Array.isArray(pool.members) || pool.members.length === 0) {
    report("strategy_pool.members 必须是非空列表，每个成员写 id 与 strategy");
  } else {
    pool.members.forEach((member: any, index: number) => {
      const id = typeof member?.id === "string" ? member.id : "";
      if (!id) report(`strategy_pool.members[${index}] 缺少 id`);
      else if (memberIds.includes(id)) report(`strategy_pool.members 的成员 id ${id} 重复`);
      if (id) memberIds.push(id);
      if (typeof member?.strategy !== "string" || !member.strategy) report(`strategy_pool.members[${index}] 缺少 strategy`);
    });
  }
  const allocation = pool.allocation && typeof pool.allocation === "object" ? pool.allocation : {};
  const method = typeof allocation.method === "string" ? allocation.method : "";
  let top = 0;
  if (!method) {
    report("strategy_pool.allocation.method 必须写 fixed 或 rotation");
  } else if (method === "fixed") {
    const ratios = allocation.ratios && typeof allocation.ratios === "object" ? allocation.ratios : null;
    if (!ratios) report("allocation.method 是 fixed，需要写 ratios（成员 id → 比例）");
    else for (const id of memberIds) if (!(id in ratios)) report(`allocation.ratios 缺少成员 ${id} 的比例（不分配请写 0）`);
  } else if (method === "rotation") {
    const factors = allocation.rotation?.factors;
    if (!Array.isArray(factors) || factors.length === 0) report("allocation.method 是 rotation，需要写 rotation.factors");
    else
      factors.forEach((factor: any, index: number) => {
        if (!rotationFactorNames.includes(String(factor?.name)))
          report(`rotation.factors[${index}] 的 name 只能是 ${rotationFactorNames.join("、")}`);
      });
    top = Number(allocation.rotation?.top);
    if (!Number.isInteger(top) || top < 1) {
      report("rotation.top 必须是不小于 1 的整数");
      top = 0;
    }
  } else {
    report("strategy_pool.allocation.method 只能是 fixed 或 rotation");
  }
  const timingWindow = Number(pool.timing?.window);
  return {
    memberIds,
    method,
    top,
    timingWindow: Number.isInteger(timingWindow) && timingWindow > 0 ? timingWindow : 0,
    step: pool.step === undefined || pool.step === null ? "" : String(pool.step)
  };
}

/** 策略池的一句话说明：3 个成员，轮动（前 1 名），择时 24 根，步长 0.2。 */
export function describePool(pool: DSLPoolPreview): string {
  const method =
    pool.method === "rotation"
      ? `轮动${pool.top ? `（前 ${pool.top} 名）` : ""}`
      : pool.method === "fixed"
        ? "固定比例"
        : "分配方式未定";
  const parts = [`${pool.memberIds.length} 个成员`, method];
  if (pool.timingWindow) parts.push(`择时 ${pool.timingWindow} 根`);
  if (pool.step) parts.push(`步长 ${pool.step}`);
  return parts.join("，");
}

function describeUniverse(universe: Record<string, any>): string {
  const parts: string[] = [];
  if (Array.isArray(universe.tags) && universe.tags.length) parts.push(`标签 ${universe.tags.join("、")}`);
  if (Array.isArray(universe.exclude_tags) && universe.exclude_tags.length)
    parts.push(`排除标签 ${universe.exclude_tags.join("、")}`);
  if (Array.isArray(universe.include) && universe.include.length) parts.push(`包含 ${universe.include.length} 个`);
  if (Array.isArray(universe.exclude) && universe.exclude.length) parts.push(`排除 ${universe.exclude.join("、")}`);
  if (universe.min_age_bars) parts.push(`上市满 ${universe.min_age_bars} 根`);
  return parts.length ? parts.join("；") : "全部标的";
}

/** 定义列表里的一行摘要。 */
export function summarizeDSL(source: string): string {
  const preview = parseDSL(source).preview;
  if (!preview) return "DSL 无法解析";
  if (preview.pool) return `${preview.bar} · 策略池：${describePool(preview.pool)}`;
  if (!preview.rules.length) return "DSL 没有规则";
  const rules = preview.rules
    .map(
      rule =>
        `${rule.name || rule.id}（${rule.type === "tranche" ? `tranche，${rule.subStrategies} 个子策略` : rule.type || "?"}）`
    )
    .join("、");
  return `${preview.bar} · ${preview.rules.length} 条规则：${rules}`;
}

/** 研究回放暂不支持策略池：回放入口据此禁用。 */
export const POOL_REPLAY_DISABLED_TEXT = "研究回放暂不支持策略池";

/** DSL 是不是策略池（解析失败的不算）。 */
export function isPoolStrategy(dslYaml: string | undefined): boolean {
  return Boolean(dslYaml && parseDSL(dslYaml).preview?.pool);
}
