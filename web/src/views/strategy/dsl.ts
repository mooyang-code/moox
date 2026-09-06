import { parseDocument } from "yaml";

export const rankedTemplate = `name: 收盘价排序示例
triggers:
  event: {name: source.ready}
data: {bar: 1h, calendar: crypto_24x7}
rules:
  rank:
    pool: [BTC-USDT-SPOT, ETH-USDT-SPOT]
    filter_before: "close > 0"
    score: "close"
    select: {top: 1}
    weight: 0.60
`;

export const signalTemplate = `name: 均线穿越示例
triggers:
  event: {name: factor.ready}
data: {bar: 1h, calendar: crypto_24x7}
rules:
  trend:
    pool: [BTC-USDT-SPOT]
    score: "close"
    select: {top: 1}
    signals:
      entry: "bars[-1].ma20 <= bars[-1].close && bars[0].ma20 > bars[0].close"
      exit: "bars[-1].ma20 >= bars[-1].close && bars[0].ma20 < bars[0].close"
    weight_each: 0.10
`;

export interface DSLDiagnostic {
  message: string;
}

export interface DSLPreview {
  name: string;
  bar: string;
  calendar: string;
  triggers: string[];
  eventName: string;
  rules: string[];
}

export function parseDSL(source: string): { preview: DSLPreview | null; diagnostics: DSLDiagnostic[] } {
  const document = parseDocument(source, { uniqueKeys: true });
  const diagnostics = [...document.errors, ...document.warnings].map(item => ({ message: item.message }));
  if (document.errors.length > 0) return { preview: null, diagnostics };
  const value = document.toJS() as Record<string, any> | null;
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return { preview: null, diagnostics: [{ message: "DSL 必须是 YAML 对象" }] };
  }
  const rules = value.rules && typeof value.rules === "object" ? Object.keys(value.rules) : [];
  const triggers = value.triggers && typeof value.triggers === "object" ? Object.keys(value.triggers) : [];
  const eventName = typeof value.triggers?.event?.name === "string" ? value.triggers.event.name : "";
  return {
    preview: {
      name: typeof value.name === "string" ? value.name : "未命名策略",
      bar: String(value.data?.bar ?? "-"),
      calendar: String(value.data?.calendar ?? "-"),
      triggers,
      eventName,
      rules
    },
    diagnostics
  };
}

export function dslName(source: string): string {
  return parseDSL(source).preview?.name || "未命名策略";
}

const builtinBarFields = new Set(["open", "high", "low", "close", "volume"]);
const expressionFunctions = new Set(["abs", "ceil", "floor", "round", "min", "max", "sum", "avg", "mean", "std", "log", "sqrt", "pow", "rank", "zscore", "true", "false", "null", "and", "or", "not", "in"]);
const expressionBuiltins = new Set([...builtinBarFields, "instrument_id", "score"]);

export function requiredBarFields(source: string): string[] {
  const fields = new Set<string>();
  for (const match of source.matchAll(/\bbars\[-?\d+\]\.([A-Za-z_][A-Za-z0-9_]*)/g)) {
    const field = match[1];
    if (!builtinBarFields.has(field)) fields.add(field);
  }
  return [...fields];
}

function expressionFields(expression: string): string[] {
  const fields = new Set<string>();
  const withoutBars = expression
    .replace(/("(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|`(?:\\.|[^`\\])*`)/g, " ")
    .replace(/\bbars\[-?\d+\]\.([A-Za-z_][A-Za-z0-9_]*)/g, (_, field: string) => {
    if (!builtinBarFields.has(field)) fields.add(field);
    return " ";
  });
  for (const match of withoutBars.matchAll(/\b([A-Za-z_][A-Za-z0-9_]*)\b/g)) {
    const field = match[1];
    const next = withoutBars.slice(match.index! + field.length).match(/^\s*\(/);
    if (!expressionFunctions.has(field.toLowerCase()) && !expressionBuiltins.has(field) && !next) fields.add(field);
  }
  return [...fields];
}

export function requiredFactorFields(source: string): string[] {
  const document = parseDocument(source);
  if (document.errors.length) return [];
  const rules = (document.toJS() as Record<string, any> | null)?.rules;
  const fields = new Set<string>();
  const expressionKeys = new Set(["score", "filter_before", "filter_after", "entry", "exit", "signal", "where"]);
  const visit = (value: unknown, key = "") => {
    if (typeof value === "string" && expressionKeys.has(key)) expressionFields(value).forEach(field => fields.add(field));
    else if (value && typeof value === "object") Object.entries(value as Record<string, unknown>).forEach(([childKey, child]) => visit(child, childKey));
  };
  visit(rules);
  return [...fields];
}
