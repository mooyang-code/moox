import type { FactorDef, FactorMember } from "@/api/factor/types";
import { completeMemberParams, deriveMemberAlias } from "@/views/factor/shared/member-params";

/** fixable 为真表示改一下参数或别名就可能通过（行保持可选）；否则是这个因子本身加不进去。 */
export type PrecheckResult = { ok: true; alias: string } | { ok: false; reason: string; fixable?: true };

const fixable = (reason: string): PrecheckResult => ({ ok: false, reason, fixable: true });

/** 计算任务里已有成员中预检用到的部分。 */
export type PrecheckMember = Pick<FactorMember, "factor_id"> & {
  alias: string;
  params?: Record<string, number>;
  /** 成员在结果数据集里的输出列名（含别名前缀）。 */
  output_columns: string[];
};

export interface PrecheckInput {
  candidate: FactorDef;
  /** 该计算任务已有的成员（含已停用）。 */
  members: PrecheckMember[];
  /** 源数据集的列名；还没加载出来时为 null，此时跳过依赖数据集的检查。 */
  sourceColumns: string[] | null;
  /** 要加入的成员的固定参数（没给的取定义的默认值）。 */
  params?: Record<string, number>;
  /** 手动指定的别名；为空时按参数生成。 */
  alias?: string;
}

const ALIAS_PATTERN = /^[A-Za-z][A-Za-z0-9_]{0,31}$/;

function sameParams(left: Record<string, number>, right: Record<string, number>) {
  const names = Object.keys(left);
  return names.length === Object.keys(right).length && names.every(name => right[name] === left[name]);
}

/**
 * 「添加因子」弹窗的预检，只用于提前置灰与提示；权威校验永远是后端 AddTaskMember（后端规格 §6.2 / D25）。
 * 同一个因子可以用不同的固定参数多次加入：没有声明参数的因子在任务里只能有一个成员，有参数的因子参数不同才算新成员。
 */
export function precheckAddFactor({ candidate, members, sourceColumns, params, alias }: PrecheckInput): PrecheckResult {
  const decls = candidate.params ?? [];
  for (const decl of decls) {
    const value = params?.[decl.name] ?? decl.default_value;
    if (!Number.isFinite(value) || value < decl.min || value > decl.max) {
      return fixable(`参数 ${decl.name} 需在 ${decl.min} ~ ${decl.max} 之间`);
    }
    if (decl.type === "int" && !Number.isInteger(value)) return fixable(`参数 ${decl.name} 必须是整数`);
  }
  const full = completeMemberParams(decls, params);
  const sameFactor = members.filter(item => item.factor_id === candidate.factor_id);
  if (!decls.length && sameFactor.length) return { ok: false, reason: "已在该计算任务中" };
  const chosen = alias?.trim() || "";
  let finalAlias = chosen;
  if (!chosen) {
    const derived = deriveMemberAlias(candidate.factor_id, decls, full);
    if (derived.tooLong) return fixable(`按参数生成的别名 ${derived.alias} 超过 32 个字符，请手动指定别名`);
    finalAlias = derived.alias;
  } else if (!ALIAS_PATTERN.test(chosen) || chosen.includes("__")) {
    return fixable("别名需字母开头，只含字母、数字、下划线，最长 32 个字符，不能含连续下划线");
  }
  const sameAlias = members.find(item => item.alias === finalAlias);
  if (sameAlias) return fixable(`别名 ${finalAlias} 已在该计算任务中`);
  const sameParamMember = sameFactor.find(item => sameParams(completeMemberParams(decls, item.params), full));
  if (sameParamMember) return fixable(`已有相同参数的成员 ${sameParamMember.alias}`);
  if (!sourceColumns) return { ok: true, alias: finalAlias };

  const source = new Set(sourceColumns);
  const missing = (candidate.input_columns || []).find(column => !source.has(column));
  if (missing) return { ok: false, reason: `输入列 ${missing} 不在源数据集中` };

  const owners = new Map<string, string>();
  for (const member of members) for (const column of member.output_columns) owners.set(column, member.alias);
  for (const output of candidate.outputs || []) {
    const column = finalAlias === candidate.factor_id ? output : `${finalAlias}_${output}`;
    if (source.has(column)) return fixable(`输出列 ${column} 与源数据集列重名`);
    const owner = owners.get(column);
    if (owner) return fixable(`输出列 ${column} 与成员 ${owner} 的输出列重名`);
  }
  return { ok: true, alias: finalAlias };
}
