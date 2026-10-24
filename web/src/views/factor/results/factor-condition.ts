import type { ViewFilterState } from "@/views/data/view-browse/view-browse-utils";

export type FactorConditionOp = "gt" | "gte" | "lt" | "lte" | "eq" | "range";

/** 一个因子条件：对某个输出列做数值比较；多个条件同时满足（且）。 */
export interface FactorCondition {
  column: string;
  op: FactorConditionOp;
  /** 非区间算子的值。 */
  value?: number;
  /** 区间的下限与上限，含端点。 */
  min?: number;
  max?: number;
}

export const CONDITION_OPS: Array<{ value: FactorConditionOp; label: string }> = [
  { value: "gt", label: ">" },
  { value: "gte", label: "≥" },
  { value: "lt", label: "<" },
  { value: "lte", label: "≤" },
  { value: "eq", label: "=" },
  { value: "range", label: "区间" }
];

const opSet = new Set<string>(CONDITION_OPS.map(item => item.value));

/** 整串解析为有限数字；空串、"12abc"、NaN、Infinity 都返回 undefined。 */
export function parseNumber(text: unknown): number | undefined {
  if (typeof text === "number") return Number.isFinite(text) ? text : undefined;
  if (typeof text !== "string" || !text.trim()) return undefined;
  const value = Number(text.trim());
  return Number.isFinite(value) ? value : undefined;
}

/** 条件本身是否完整合法（数值有限，区间下限不大于上限）。 */
export function isValidCondition(condition: FactorCondition) {
  if (!condition.column || !opSet.has(condition.op)) return false;
  if (condition.op === "range") {
    const min = parseNumber(condition.min);
    const max = parseNumber(condition.max);
    return min !== undefined && max !== undefined && min <= max;
  }
  return parseNumber(condition.value) !== undefined;
}

export function toFilterState(condition: FactorCondition): ViewFilterState {
  const base = { fieldName: condition.column, valueType: "FIELD_VALUE_TYPE_DOUBLE" as const };
  if (condition.op === "range") {
    return { ...base, operator: "range", startValue: String(condition.min), endValue: String(condition.max) };
  }
  return { ...base, operator: condition.op, value: String(condition.value) };
}

export function conditionText(condition: FactorCondition, label = condition.column) {
  if (condition.op === "range") return `${label} 区间 ${condition.min}～${condition.max}`;
  const symbol = CONDITION_OPS.find(item => item.value === condition.op)?.label ?? condition.op;
  return `${label} ${symbol} ${condition.value}`;
}

/** 编进 ?fc=：每个条件是 [列, 算子, 值] 或 [列, "range", 下限, 上限]。 */
export function encodeConditions(conditions: FactorCondition[]) {
  if (!conditions.length) return "";
  return JSON.stringify(
    conditions.map(item =>
      item.op === "range" ? [item.column, item.op, item.min, item.max] : [item.column, item.op, item.value]
    )
  );
}

/**
 * 解码 ?fc= 并逐条校验：列必须在当前有效范围里、算子合法、数值有限、区间有序、同一条件不重复。
 * 返回保留下来的条件和丢弃的条数。
 */
export function decodeConditions(raw: unknown, allowedColumns: Set<string>) {
  const conditions: FactorCondition[] = [];
  let dropped = 0;
  if (typeof raw !== "string" || !raw) return { conditions, dropped };
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return { conditions, dropped: 1 };
  }
  if (!Array.isArray(parsed)) return { conditions, dropped: 1 };
  const seen = new Set<string>();
  for (const entry of parsed) {
    const condition = entryToCondition(entry);
    const key = condition ? encodeConditions([condition]) : "";
    if (!condition || !allowedColumns.has(condition.column) || !isValidCondition(condition) || seen.has(key)) {
      dropped += 1;
      continue;
    }
    seen.add(key);
    conditions.push(condition);
  }
  return { conditions, dropped };
}

function entryToCondition(entry: unknown): FactorCondition | undefined {
  if (!Array.isArray(entry) || typeof entry[0] !== "string" || typeof entry[1] !== "string") return undefined;
  const [column, op] = entry as [string, FactorConditionOp];
  if (op === "range") {
    const min = parseNumber(entry[2]);
    const max = parseNumber(entry[3]);
    return min === undefined || max === undefined ? undefined : { column, op, min, max };
  }
  const value = parseNumber(entry[2]);
  return value === undefined ? undefined : { column, op, value };
}
