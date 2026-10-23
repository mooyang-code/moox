/**
 * 「插入模板」的时序因子定义。params 声明可调参数（类型、范围、默认值）：策略可以每期在范围内调整，
 * 输出列名固定、不随参数变化；回看按范围的上限推导。
 */
export const TIMESERIES_SPEC_TEMPLATE = [
  "# 时序因子：每个标的、每个 series_tag 各自沿时间计算。",
  "params:",
  "  window: {type: int, min: 2, max: 120, default: 20}",
  "let:",
  "  ma: ts_mean(close, window)",
  "outputs:",
  "  bias: close / ma - 1",
  ""
].join("\n");

/** 截面因子定义：同一时刻、同一 series_tag 的全部标的之间横向计算（排名、标准化）。 */
export const CROSS_SECTION_SPEC_TEMPLATE = [
  "# 截面因子：同一时刻的全部标的之间排名，结果在 (0, 1]。",
  "params:",
  "  window: {type: int, min: 2, max: 120, default: 20}",
  "outputs:",
  "  momentum_rank: cs_rank(close / delay(close, window) - 1)",
  ""
].join("\n");

export type FactorTemplateKind = "timeseries" | "cross_section";

export function specTemplate(kind: FactorTemplateKind) {
  return kind === "cross_section" ? CROSS_SECTION_SPEC_TEMPLATE : TIMESERIES_SPEC_TEMPLATE;
}

/** 内置模板原样（未改动）时可以直接替换，不用二次确认。 */
export function isBuiltinTemplate(spec: string) {
  return spec === TIMESERIES_SPEC_TEMPLATE || spec === CROSS_SECTION_SPEC_TEMPLATE;
}
