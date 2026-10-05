/** 「插入模板」的 compute(df, params, context) 骨架。 */
export const FACTOR_SOURCE_TEMPLATE = [
  "def compute(df, params, context):",
  "    close = df['close']",
  "    result = df[['data_time', 'series_tag']].copy()",
  "    for window in params['windows']:",
  "        average = close.rolling(window, min_periods=1).mean()",
  "        result[f'bias_{window}'] = close / average - 1",
  "    return result",
  ""
].join("\n");
