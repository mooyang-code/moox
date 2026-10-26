import type { FactorDef, FactorInput, FactorType, ValidateFactorSpecRsp } from "@/api/factor/types";
import type { DatasetColumn } from "@/api/storage/types";
import { TIMESERIES_SPEC_TEMPLATE } from "./editor-template";

export const RESERVED_COLUMNS = ["subject_id", "freq", "data_time", "series_tag"];

const IDENTIFIER = /^[A-Za-z_][A-Za-z0-9_]*$/;
/** 与后端 validateName 一致：1～64 个字符、首尾无空白、不含控制字符。 */
const NAME_MAX = 64;

export const isIdentifier = (value: string) => IDENTIFIER.test(value);

function isSystemColumn(column: DatasetColumn) {
  const origin = column.origin_type;
  return origin === 3 || (typeof origin === "string" && origin.endsWith("_SYSTEM"));
}

/** 与后端一致：active、非系统、非保留列才能作为因子输入（参考数据集的提示用）。 */
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

/** 编辑器表单：定义只有标识、展示名、表达式与 allow_partial_universe；其余字段由编译推导。 */
export interface FactorFormState {
  factor_id: string;
  name: string;
  spec: string;
  allow_partial_universe: boolean;
  /** 因子分类（空表示不改：新建取“未分类”，编辑保持原分类）。 */
  category_id: string;
}

export function blankFactorForm(): FactorFormState {
  return { factor_id: "", name: "", spec: TIMESERIES_SPEC_TEMPLATE, allow_partial_universe: false, category_id: "" };
}

export function formFromDefinition(def: FactorDef): FactorFormState {
  return {
    factor_id: def.factor_id,
    name: def.name,
    spec: def.spec || "",
    allow_partial_universe: Boolean(def.allow_partial_universe),
    category_id: def.category_id || ""
  };
}

/** 编译结果的展示状态：idle 尚未校验（或内容为空），checking 校验中，valid / invalid 是最近一次结果，error 是接口失败。 */
export type CompileState =
  | { status: "idle" }
  | { status: "checking" }
  | { status: "valid"; spec: string; result: ValidateFactorSpecRsp }
  | { status: "invalid"; spec: string; result: ValidateFactorSpecRsp }
  | { status: "error"; message: string };

/** 最近一次校验是否针对当前内容且通过（allow_partial_universe 也参与，它影响 definition_hash）。 */
export function compiledFor(state: CompileState, form: FactorFormState) {
  return state.status === "valid" && state.spec === specKey(form);
}

export function specKey(form: FactorFormState) {
  return `${form.allow_partial_universe ? 1 : 0}\u0000${form.spec}`;
}

/** 编译诊断的可读文本：字段路径与行列在前。 */
/**
 * 编译诊断的可读文本。doc_line 是 spec 文档里的行（从 1 开始），line/column 是该字段表达式内部的位置；
 * 表达式只有一行时只显示列。
 */
export function diagnosticText(result: ValidateFactorSpecRsp | undefined) {
  const diagnostic = result?.diagnostic;
  if (!diagnostic) return "";
  const inExpression = [
    diagnostic.line && diagnostic.line > 1 ? `表达式第 ${diagnostic.line} 行` : "",
    diagnostic.column ? `第 ${diagnostic.column} 列` : ""
  ].join("");
  const where = [diagnostic.path, diagnostic.doc_line ? `定义第 ${diagnostic.doc_line} 行` : "", inExpression]
    .filter(Boolean)
    .join("，");
  return where ? `${where}：${diagnostic.message}` : diagnostic.message;
}

/** 编辑器里要标红的行：诊断在 spec 文档中的行。 */
export function diagnosticDocLine(result: ValidateFactorSpecRsp | undefined) {
  return result?.diagnostic?.doc_line || 0;
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

function checkName(raw: string) {
  const name = raw.trim();
  if (!name) return "展示名不能为空";
  if ([...name].length > NAME_MAX) return `展示名最多 ${NAME_MAX} 个字符`;
  if (/[\u0000-\u001f\u007f]/.test(name)) return "展示名不能包含控制字符";
  return "";
}

function compileMessage(state: CompileState, form: FactorFormState) {
  if (!form.spec.trim()) return "表达式定义不能为空";
  switch (state.status) {
    case "valid":
      return state.spec === specKey(form) ? "" : "定义已修改，等待重新校验";
    case "invalid":
      return state.spec === specKey(form) ? diagnosticText(state.result) || "定义不合法" : "定义已修改，等待重新校验";
    case "error":
      return `校验失败：${state.message}`;
    default:
      return "正在校验定义";
  }
}

/**
 * 保存前检查清单。表达式的语法、类型、回看与输出列由服务端编译（ValidateFactorSpec）判定，前端不重复实现；
 * 「输入列 ⊆ 源数据集的数值列」「输出列不与源列、其他因子冲突」属于加入计算任务时的校验，不在这里。
 */
export function factorChecklist(form: FactorFormState, compile: CompileState): ChecklistItem[] {
  const id = form.factor_id.trim();
  const derivedType = compiledFor(compile, form) && compile.status === "valid" ? compile.result.factor_type : undefined;
  return [
    item(
      "factor_id",
      "因子 ID 合法（全局唯一由后端保存时检查）",
      !id ? "因子 ID 不能为空" : isIdentifier(id) ? "" : "因子 ID 必须以字母或下划线开头，仅含字母数字下划线"
    ),
    item("name", "展示名 1～64 个字符", checkName(form.name)),
    item("spec", "表达式编译通过", compileMessage(compile, form)),
    item(
      "allow_partial_universe",
      "仅截面因子可允许部分对象",
      form.allow_partial_universe && derivedType && derivedType !== "cross_section" ? "只有截面因子可以允许部分对象" : ""
    )
  ];
}

/** 未通过项的原因列表（用于禁用保存按钮时的说明）。 */
export function checklistBlockers(items: ChecklistItem[]) {
  return items.filter(entry => !entry.ok).map(entry => entry.message);
}

/** 提交体：不带派生字段、task_id、status，也不带参考数据集。 */
export function buildFactorPayload(form: FactorFormState, factorType?: FactorType): FactorInput {
  const payload: FactorInput = {
    factor_id: form.factor_id.trim(),
    name: form.name.trim(),
    spec: form.spec,
    allow_partial_universe: factorType === "cross_section" ? form.allow_partial_universe : false
  };
  if (form.category_id) payload.category_id = form.category_id;
  return payload;
}

/** 函数参考面板的分组与搜索。 */
export const FUNCTION_CATEGORY_LABELS: Record<string, string> = {
  elementwise: "逐元素",
  timeseries: "时序",
  cross_section: "截面"
};

export function filterFunctions<T extends { name: string; doc: string; signature: string }>(functions: T[], keyword: string) {
  const query = keyword.trim().toLowerCase();
  if (!query) return functions;
  return functions.filter(
    fn =>
      fn.name.toLowerCase().includes(query) || fn.signature.toLowerCase().includes(query) || fn.doc.toLowerCase().includes(query)
  );
}
