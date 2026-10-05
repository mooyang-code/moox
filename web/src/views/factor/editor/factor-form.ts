import type { FactorDef, FactorInput, FactorType } from "@/api/factor/types";
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

/** 与后端 businessColumns 一致：active、非系统、非保留列才能作为因子输入（参考数据集的提示用）。 */
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

/** 编辑器表单：只有定义本身的字段，没有 set_id / status / 数据集。 */
export interface FactorFormState {
  factor_id: string;
  name: string;
  factor_type: FactorType;
  lookback_periods: number;
  allow_partial_universe: boolean;
  input_columns: string[];
  outputs: string[];
  params_json: string;
  source_code: string;
}

export function blankFactorForm(): FactorFormState {
  return {
    factor_id: "",
    name: "",
    factor_type: "timeseries",
    lookback_periods: 200,
    allow_partial_universe: false,
    input_columns: [],
    outputs: [],
    params_json: `{"windows":[20]}`,
    source_code: ""
  };
}

export function formFromDefinition(def: FactorDef): FactorFormState {
  return {
    factor_id: def.factor_id,
    name: def.name,
    factor_type: def.factor_type,
    lookback_periods: def.lookback_periods,
    allow_partial_universe: Boolean(def.allow_partial_universe),
    input_columns: [...(def.input_columns || [])],
    outputs: [...(def.outputs || [])],
    params_json: def.params_json || "{}",
    source_code: def.source_code || ""
  };
}

export interface ChecklistItem {
  key: string;
  label: string;
  ok: boolean;
  /** 未通过时的原因；通过时为空串。 */
  message: string;
}

function item(key: string, label: string, message: string): ChecklistItem {
  return { key, label, ok: !message, message };
}

function checkColumns(columns: string[], field: string, reserved: Set<string> | null) {
  if (!columns.length) return `${field}不能为空`;
  const seen = new Set<string>();
  for (const column of columns) {
    if (!isIdentifier(column)) return `${field} ${column} 不是合法列名（字母或下划线开头，仅含字母数字下划线）`;
    if (reserved?.has(column)) return `${field} ${column} 是系统保留列`;
    if (seen.has(column)) return `${field} ${column} 重复`;
    seen.add(column);
  }
  return "";
}

function checkParams(raw: string) {
  try {
    validateFactorParamsJSON(raw);
    return "";
  } catch (error) {
    return error instanceof SyntaxError ? "参数必须是合法 JSON" : "参数必须是 JSON object";
  }
}

/**
 * 保存前检查清单，对应后端规格 §6.1 的静态校验（不依赖数据集）。
 * 「输入列 ⊆ 源数据集列」「输出列不与源数据集 / 其他因子冲突」属于加入计算任务时的校验，不在这里。
 */
export function factorChecklist(form: FactorFormState): ChecklistItem[] {
  const reserved = new Set(RESERVED_COLUMNS);
  const id = form.factor_id.trim();
  return [
    item(
      "factor_id",
      "因子 ID 合法（全局唯一由后端保存时检查）",
      !id ? "因子 ID 不能为空" : isIdentifier(id) ? "" : "因子 ID 必须以字母或下划线开头，仅含字母数字下划线"
    ),
    item("name", "模块名不为空", form.name.trim() ? "" : "模块名不能为空"),
    item("input_columns", "输入列非空、名称合法、无重复", checkColumns(form.input_columns, "输入列", null)),
    item("outputs", "输出列非空、不含系统保留列、无重复", checkColumns(form.outputs, "输出列", reserved)),
    item("params_json", "参数是合法 JSON object", checkParams(form.params_json)),
    item(
      "lookback_periods",
      "回看周期不小于 1",
      Number.isInteger(form.lookback_periods) && form.lookback_periods >= 1 ? "" : "回看周期必须是不小于 1 的整数"
    ),
    item(
      "allow_partial_universe",
      "仅横截面因子可允许部分对象",
      form.allow_partial_universe && form.factor_type !== "cross_section" ? "只有横截面因子可以允许部分对象" : ""
    ),
    item("source_code", "源码不为空（语法由后端试加载校验）", form.source_code.trim() ? "" : "源码不能为空")
  ];
}

/** 未通过项的原因列表（用于禁用保存按钮时的说明）。 */
export function checklistBlockers(items: ChecklistItem[]) {
  return items.filter(entry => !entry.ok).map(entry => entry.message);
}

/** 提交体：不带 set_id、status，也不带参考数据集。 */
export function buildFactorPayload(form: FactorFormState): FactorInput {
  return {
    factor_id: form.factor_id.trim(),
    name: form.name.trim(),
    factor_type: form.factor_type,
    source_code: form.source_code,
    input_columns: [...form.input_columns],
    outputs: [...form.outputs],
    params_json: validateFactorParamsJSON(form.params_json),
    lookback_periods: form.lookback_periods,
    allow_partial_universe: form.factor_type === "cross_section" ? form.allow_partial_universe : false
  };
}
