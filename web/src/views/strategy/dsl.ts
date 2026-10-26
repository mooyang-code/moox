import { parseDocument } from "yaml";

/** 截面选股模板：只用因子库已有的列（Binance 现货因子结果 View）；bias = close / MA（窗口默认 20）。 */
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
 * 动态调参模板：策略声明自己的因子成员（同一定义可以用不同别名，参数各自调整），启用实例时自动建立私有因子任务；
 * 规则按 <别名>_<输出名> 引用因子值，按 <别名>__<参数名> 读取这一期实际用的参数；factor_params 每期求出下一期的参数
 * （default 是各标的取值的中位数，per_subject 可选，与统一值不同的标的进入覆盖；没有写出的参数取因子定义的默认值）。
 */
export const tunedTemplate = `name: tuned_bias
bar: 1m

factors:
  fast: {factor: Bias}
  slow: {factor: Bias}
  vol: {factor: ZfStd}

factor_params:
  fast:
    window:
      default: "20"
      per_subject: "10 + 30 * rank(vol_zf_std)"
  slow:
    window: "60"

rules:
  - id: bias_spread
    name: 快慢偏离差
    type: rank
    filter: "fast_bias > 0 && slow_bias > 0"
    score: "rank(fast_bias) - rank(slow_bias)"
    select: {top: 5}
    weight: {total: 1, method: equal, cap: 0.3}

portfolio:
  leverage: 1
  min_universe: 20
  max_missing: 0.2
`;

export interface DSLDiagnostic {
  message: string;
}

export interface DSLRulePreview {
  id: string;
  name: string;
  type: string;
}

export interface DSLPreview {
  name: string;
  bar: string;
  rules: DSLRulePreview[];
  universe: string;
  leverage: string;
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
  if (!Array.isArray(value.rules) || value.rules.length === 0) {
    diagnostics.push({ message: "rules 必须是非空列表，每条规则包含 id 与 type" });
  } else {
    value.rules.forEach((rule: any, index: number) => {
      const id = typeof rule?.id === "string" ? rule.id : "";
      const type = typeof rule?.type === "string" ? rule.type : "";
      if (!id) diagnostics.push({ message: `rules[${index}] 缺少 id` });
      if (type !== "rank" && type !== "signal") diagnostics.push({ message: `rules[${index}] 的 type 只能是 rank 或 signal` });
      rules.push({ id: id || `#${index + 1}`, name: typeof rule?.name === "string" ? rule.name : "", type });
    });
  }
  const universe = value.universe && typeof value.universe === "object" ? describeUniverse(value.universe) : "全部标的";
  return {
    preview: {
      name: typeof value.name === "string" ? value.name : "未命名策略",
      bar: value.bar === undefined ? "由 View 决定" : String(value.bar),
      rules,
      universe,
      leverage: value.portfolio?.leverage === undefined ? "1" : String(value.portfolio.leverage)
    },
    diagnostics
  };
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
  if (!preview.rules.length) return "DSL 没有规则";
  const rules = preview.rules.map(rule => `${rule.name || rule.id}（${rule.type || "?"}）`).join("、");
  return `${preview.bar} · ${preview.rules.length} 条规则：${rules}`;
}
