import type { DatasetColumn } from "@/api/storage/types";

export const RESERVED_COLUMNS = ["subject_id", "freq", "data_time", "series_tag"];

const IDENTIFIER = /^[A-Za-z_][A-Za-z0-9_]*$/;

export const isIdentifier = (value: string) => IDENTIFIER.test(value);

export function validateFactorParamsJSON(raw: string): string {
  const normalized = raw.trim() || "{}";
  const params: unknown = JSON.parse(normalized);
  if (!params || Array.isArray(params) || typeof params !== "object") {
    throw new TypeError("params must be a JSON object");
  }
  return normalized;
}

function isSystemColumn(column: DatasetColumn) {
  const origin = column.origin_type;
  return origin === 3 || (typeof origin === "string" && origin.endsWith("_SYSTEM"));
}

/** 与后端 businessColumns 一致：active、非系统、非保留列才能作为因子输入。 */
export function sourceInputColumns(columns: DatasetColumn[]) {
  const reserved = new Set(RESERVED_COLUMNS);
  return columns
    .filter(
      column =>
        column.column_name && !isSystemColumn(column) && (!column.status || column.status.toLowerCase().includes("active"))
    )
    .map(column => column.column_name)
    .filter(name => !reserved.has(name))
    .sort();
}

export interface OutputCheck {
  sourceColumns: string[];
  siblingOutputs: string[];
}

/** 返回第一个违反后端约束的输出列问题；通过时为空串。 */
export function checkOutputs(outputs: string[], { sourceColumns, siblingOutputs }: OutputCheck) {
  if (!outputs.length) return "输出列不能为空";
  const reserved = new Set(RESERVED_COLUMNS);
  const source = new Set(sourceColumns);
  const sibling = new Set(siblingOutputs);
  const seen = new Set<string>();
  for (const output of outputs) {
    if (!isIdentifier(output)) return `输出列 ${output} 不是合法列名（字母或下划线开头，仅含字母数字下划线）`;
    if (reserved.has(output)) return `输出列 ${output} 是保留列`;
    if (seen.has(output)) return `输出列 ${output} 重复`;
    seen.add(output);
    if (source.has(output)) return `输出列 ${output} 与源数据集列重名`;
    if (sibling.has(output)) return `输出列 ${output} 与同因子集其他因子的输出重名`;
  }
  return "";
}

export function checkInputs(inputs: string[], sourceColumns: string[]) {
  if (!inputs.length) return "输入列不能为空";
  const source = new Set(sourceColumns);
  const missing = inputs.find(input => !source.has(input));
  return missing ? `输入列 ${missing} 不在源数据集中` : "";
}

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
