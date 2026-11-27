import { parseDocument } from "yaml";

/** 截面选股模板：假设关联的计算任务里有默认参数的 bias、bias_q、quote_volume_mean、quote_volume_mean_q 成员（列名就是成员键）；bias = close / MA（窗口默认 20）。 */
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
    type: rank
    pool: [BTC-USDT]
    allow_empty: true
    # 写了 allow_empty 后缺数不再整期跳过：BTC 偶尔缺数先冻结保持，连续超过 3 根才出场
    on_missing: {keep_bars: 3}
    score: "0"
    select: {top: 10000}
    hold:
      entry: "bars[-1].bias <= 1 && bars[0].bias > 1"
      exit: "bars[0].bias < 1"
    weight: {total: 0.2}

portfolio:
  leverage: 1
  max_weight: 0.3
  min_universe: 20
  max_missing: 0.2
`;

/** 固定标的择时模板：BTC、ETH 站上 20 期均线时持有。score 取常数、select.top 取足够大，表示池里满足进出场条件的都持有。 */
export const timingTemplate = `name: majors_trend
bar: 1m

rules:
  - id: majors_trend
    name: 主流币均线择时
    type: rank
    pool: [BTC-USDT, ETH-USDT]
    score: "0"
    select: {top: 10000}
    hold:
      entry: "bars[-1].bias <= 1 && bars[0].bias > 1"
      exit: "bars[0].bias < 1"
    weight: {total: 0.6}

portfolio:
  leverage: 1
`;

/**
 * 动态调参模板：策略不声明因子，规则直接引用关联计算任务的列名（成员键与列名规则见因子文档）；
 * 计算任务里要有可变成员 ma__var（加成员时选“可变”），factor_params 每期求出它下一期的参数
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
# 创建实例时绑定计算任务：现货、永续各一个（两个任务的列名要一致）；
# 需要逐期调因子参数时也可以写 factor_params。
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
# 现货、永续成员必须显式写 market（股票成员不写，池绑定 equity 任务）；bar 要与池一致，不能是策略池，不能用 factor_params。
# 池本身不写 market、universe、factor_params，必须写 bar。
# 池要绑定的计算任务是各成员所需市场的并集：成员都是现货就绑一个现货任务，混有永续就现货、永续各绑一个。
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
  # fees: {spot_bps: 10, swap_bps: 5, equity_bps: 3}，永续计资金费再写 funding_column: funding_rate

portfolio:
  leverage: 1
  max_weight: 0.1
`;

export interface DSLRulePreview {
  id: string;
  name: string;
  type: string;
  /** 固定标的择时（显式 pool 加 hold）；不是时不出现。 */
  timing?: boolean;
  /** 规则级市场（spot 或 swap）；没写时不出现。 */
  market?: string;
}

/** 规则级市场的中文名。 */
export const RULE_MARKET_LABELS: Record<string, string> = { spot: "现货", swap: "永续" };

/** 策略池的只读展示预览：成员、分配方式、择时与步长。 */
export interface DSLPoolPreview {
  memberIds: string[];
  /** fixed、rotation、drift，或写错的原文；没写为空串。 */
  method: string;
  /** 轮动取前几名；fixed 或没写为 0。 */
  top: number;
  /** 择时窗口；没写择时为 0。 */
  timingWindow: number;
  /** 步长上限的原文；没写为空串。 */
  step: string;
  /** drift 的漂移起点（allocation.start 或 allocation.initial.at 的原文）；没写时不出现。 */
  driftStart?: string;
  /** 写了的组合约束与条件转移、波动冻结（说明文字，书写顺序固定）；都没写时不出现。 */
  extras?: string[];
}

export interface DSLPreview {
  name: string;
  bar: string;
  rules: DSLRulePreview[];
  universe: string;
  leverage: string;
  /** 市场模式的说明；没写 market 时是“按计算任务的市场”，策略池是成员市场的并集。 */
  market: string;
  /** 策略池策略才有；此时 rules 为空。 */
  pool?: DSLPoolPreview;
  /** 写了 factor_params（策略逐期求出可变成员下一期的参数）时为 true；回放只能用“因子现算”来源。没写时不出现。 */
  tuned?: boolean;
}

export interface DSLParseResult {
  preview: DSLPreview | null;
  syntaxError: string;
  syntaxErrorLine: number;
}

/** DSL 是不是策略池（写了 strategy_pool）：只用于展示（绑定表按池的样式显示）；解析不了按不是处理。 */
export function isPoolDsl(source: string): boolean {
  return Boolean(parseDSL(source).preview?.pool);
}

/**
 * 极简只读 YAML 解析，仅用于生成列表摘要与编辑器侧栏展示预览；
 * 全部 DSL 语义、规则、策略池约束与表达式校验统一由服务端 ValidateStrategy 负责。
 */
export function parseDSL(source: string): DSLParseResult {
  const document = parseDocument(source, { uniqueKeys: true });
  if (document.errors.length > 0) {
    const first = document.errors[0];
    return {
      preview: null,
      syntaxError: first.message,
      syntaxErrorLine: first.linePos?.[0]?.line ?? 0
    };
  }
  let value: Record<string, any> | null;
  try {
    value = document.toJS() as Record<string, any> | null;
  } catch (error) {
    // 例如未解析的别名：document.errors 里没有，但转换成对象时会抛异常。
    return {
      preview: null,
      syntaxError: error instanceof Error ? error.message : "YAML 无法解析",
      syntaxErrorLine: 0
    };
  }
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return { preview: null, syntaxError: "DSL 必须是 YAML 对象", syntaxErrorLine: 0 };
  }
  const hasPool = value.strategy_pool !== undefined && value.strategy_pool !== null;
  const pool = hasPool ? extractPoolPreview(value.strategy_pool) : undefined;
  const rules: DSLRulePreview[] =
    !hasPool && Array.isArray(value.rules)
      ? value.rules.map((rule: any, index: number) => {
          const id = typeof rule?.id === "string" ? rule.id : "";
          const type = typeof rule?.type === "string" ? rule.type : "";
          const preview: DSLRulePreview = {
            id: id || `#${index + 1}`,
            name: typeof rule?.name === "string" ? rule.name : "",
            type
          };
          if (rule?.pool !== undefined && rule?.hold !== undefined) preview.timing = true;
          if (typeof rule?.market === "string" && rule.market in RULE_MARKET_LABELS) preview.market = rule.market;
          return preview;
        })
      : [];
  const universe = [
    value.universe && typeof value.universe === "object" ? describeUniverse(value.universe) : "全部标的",
    ...describeRuleUniverses(value.rules)
  ].join("；");
  return {
    preview: {
      name: typeof value.name === "string" ? value.name : "未命名策略",
      bar: value.bar === undefined ? "由 View 决定" : String(value.bar),
      rules,
      universe,
      leverage: value.portfolio?.leverage === undefined ? "1" : String(value.portfolio.leverage),
      // 策略池不写 market：池涉及的市场是各成员所需市场的并集，选币和下单按成员自己的 market
      market: hasPool ? "成员市场的并集（各成员按自己的 market）" : describeMarket(value.market),
      ...(pool ? { pool } : {}),
      ...(value.factor_params !== undefined ? { tuned: true } : {})
    },
    syntaxError: "",
    syntaxErrorLine: 0
  };
}

/** 从服务端 ValidateStrategy 诊断文案里提取首个 YAML 1-based 行号（如“第 12 行附近”或“yaml: line 12”）；忽略表达式内部的行列号，无行号时返回 0。 */
export function extractDiagnosticLine(diagnostics: readonly string[]): number {
  for (const message of diagnostics) {
    const match = /第\s*(\d+)\s*行(?!\s*第\s*\d+\s*列)|\byaml:\s*line\s+(\d+)\b/i.exec(message);
    const line = Number(match?.[1] || match?.[2] || 0);
    if (Number.isInteger(line) && line > 0) return line;
  }
  return 0;
}

const marketSelectText: Record<string, string> = { spot: "现货", swap: "永续", both: "现货与永续" };

/** market 的说明：没写时按计算任务的市场；写了就给出选币范围与同一个币两边都有时的下单市场。 */
function describeMarket(market: unknown): string {
  if (!market || typeof market !== "object" || Array.isArray(market)) return "按计算任务的市场";
  const { select, prefer } = market as Record<string, unknown>;
  const text = (value: unknown) => marketSelectText[String(value)] ?? String(value);
  const parts = [`选币范围 ${select === undefined ? "?" : text(select)}`];
  if (prefer !== undefined && prefer !== select) parts.push(`优先在${text(prefer)}下单`);
  return parts.join("，");
}

function extractPoolPreview(pool: unknown): DSLPoolPreview | undefined {
  if (!pool || typeof pool !== "object" || Array.isArray(pool)) return undefined;
  const raw = pool as Record<string, any>;
  const memberIds: string[] = [];
  if (Array.isArray(raw.members)) {
    for (const member of raw.members) {
      const id = typeof member?.id === "string" ? member.id : "";
      if (id && !memberIds.includes(id)) memberIds.push(id);
    }
  }
  const allocation = raw.allocation && typeof raw.allocation === "object" ? raw.allocation : {};
  const method = typeof allocation.method === "string" ? allocation.method : "";
  const rawTop = Number(allocation.rotation?.top);
  const top = method === "rotation" && Number.isInteger(rawTop) && rawTop >= 1 ? rawTop : 0;
  const timingWindow = Number(raw.timing?.window);
  const driftStart = allocation.start ?? allocation.initial?.at;
  const extras = [
    allocation.switch !== undefined && "条件转移",
    raw.exposure !== undefined && "净敞口区间",
    raw.gross !== undefined && "总杠杆区间",
    raw.freeze !== undefined && "波动冻结"
  ].filter((item): item is string => Boolean(item));
  return {
    memberIds,
    method,
    top,
    timingWindow: Number.isInteger(timingWindow) && timingWindow > 0 ? timingWindow : 0,
    step: raw.step === undefined || raw.step === null ? "" : String(raw.step),
    ...(method === "drift" && driftStart !== undefined && driftStart !== null ? { driftStart: String(driftStart) } : {}),
    ...(extras.length ? { extras } : {})
  };
}

/** 策略池的一句话说明：3 个成员，轮动（前 1 名），择时 24 根，步长 0.2；drift 写起点，再列出组合约束与冻结。 */
export function describePool(pool: DSLPoolPreview): string {
  const method =
    pool.method === "rotation"
      ? `轮动${pool.top ? `（前 ${pool.top} 名）` : ""}`
      : pool.method === "fixed"
        ? "固定比例"
        : pool.method === "drift"
          ? `漂移分配${pool.driftStart ? `（起点 ${pool.driftStart}）` : ""}`
          : "分配方式未定";
  const parts = [`${pool.memberIds.length} 个成员`, method];
  if (pool.timingWindow) parts.push(`择时 ${pool.timingWindow} 根`);
  if (pool.step) parts.push(`步长 ${pool.step}`);
  parts.push(...(pool.extras ?? []));
  return parts.join("，");
}

function describeUniverse(universe: Record<string, any>): string {
  const parts: string[] = [];
  if (Array.isArray(universe.tags) && universe.tags.length) parts.push(`标签 ${universe.tags.join("、")}`);
  if (Array.isArray(universe.exclude_tags) && universe.exclude_tags.length)
    parts.push(`排除标签 ${universe.exclude_tags.join("、")}`);
  if (Array.isArray(universe.include) && universe.include.length) parts.push(`包含 ${universe.include.length} 个`);
  if (Array.isArray(universe.exclude) && universe.exclude.length) parts.push(`排除 ${universe.exclude.join("、")}`);
  parts.push(...describeAges(universe));
  return parts.length ? parts.join("；") : "全部标的";
}

/** 年龄类字段的说明：上市满 N 根、上市不满 N 根（只保留新标的）、各市场都判断。 */
function describeAges(universe: Record<string, any>): string[] {
  const parts: string[] = [];
  if (universe.min_age_bars) parts.push(`上市满 ${universe.min_age_bars} 根`);
  if (universe.max_age_bars) parts.push(`上市不满 ${universe.max_age_bars} 根`);
  if (universe.age_markets === "all") parts.push("年龄按全部市场判断");
  return parts;
}

/** 规则级 universe 的说明：写了的字段覆盖策略级。 */
function describeRuleUniverses(rules: unknown): string[] {
  if (!Array.isArray(rules)) return [];
  return rules
    .filter(rule => rule?.universe && typeof rule.universe === "object")
    .map(rule => {
      const universe = rule.universe as Record<string, any>;
      const parts: string[] = [];
      if (Array.isArray(universe.tags) && universe.tags.length) parts.push(`标签 ${universe.tags.join("、")}`);
      if (Array.isArray(universe.exclude_tags) && universe.exclude_tags.length)
        parts.push(`排除标签 ${universe.exclude_tags.join("、")}`);
      if (Array.isArray(universe.include) && universe.include.length) parts.push(`包含 ${universe.include.length} 个`);
      if (Array.isArray(universe.exclude) && universe.exclude.length) parts.push(`排除 ${universe.exclude.join("、")}`);
      parts.push(...describeAges(universe));
      return `规则 ${rule.id ?? "?"}：${parts.join("，") || "沿用策略级"}${rule.allow_empty ? "（允许为空）" : ""}`;
    });
}

/** 定义列表里的一行摘要。 */
export function summarizeDSL(source: string): string {
  const preview = parseDSL(source).preview;
  if (!preview) return "DSL 无法解析";
  if (preview.pool) return `${preview.bar} · 策略池：${describePool(preview.pool)}`;
  if (!preview.rules.length) return "DSL 没有规则";
  const rules = preview.rules
    .map(rule => `${rule.name || rule.id}（${rule.type || "?"}${rule.market ? `，${RULE_MARKET_LABELS[rule.market]}` : ""}）`)
    .join("、");
  return `${preview.bar} · ${preview.rules.length} 条规则：${rules}`;
}
