/** 「插入模板」的 compute(df, params, context) 骨架：时序因子，按 series_tag 分组滚动。 */
export const FACTOR_SOURCE_TEMPLATE = [
  "def compute(df, params, context):",
  "    result = df[['data_time', 'series_tag']].copy()",
  "    close = df.groupby('series_tag', sort=False)['close']",
  "    for window in params['windows']:",
  "        average = close.transform(lambda values: values.rolling(window, min_periods=1).mean())",
  "        result[f'bias_{window}'] = df['close'] / average - 1",
  "    return result",
  ""
].join("\n");

/** 截面因子骨架：输入输出都带 subject_id，同一 data_time 内按标的做横向排名。 */
export const CROSS_SECTION_SOURCE_TEMPLATE = [
  "def compute(df, params, context):",
  "    result = df[['data_time', 'series_tag', 'subject_id']].copy()",
  "    result['close_rank'] = df.groupby(['data_time', 'series_tag'])['close'].rank(pct=True)",
  "    return result",
  ""
].join("\n");

/** 模板配套的声明：输入列、输出列与参数要和模板源码一致，否则保存后计算会报 outputs mismatch。 */
export interface FactorTemplate {
  source: string;
  inputColumns: string[];
  outputs: string[];
  paramsJson: string;
}

export const TIMESERIES_TEMPLATE: FactorTemplate = {
  source: FACTOR_SOURCE_TEMPLATE,
  inputColumns: ["close"],
  outputs: ["bias_20"],
  paramsJson: `{"windows":[20]}`
};

export const CROSS_SECTION_TEMPLATE: FactorTemplate = {
  source: CROSS_SECTION_SOURCE_TEMPLATE,
  inputColumns: ["close"],
  outputs: ["close_rank"],
  paramsJson: "{}"
};
