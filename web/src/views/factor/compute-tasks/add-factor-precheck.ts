import type { FactorDef } from "@/api/factor/types";

export type PrecheckResult = { ok: true } | { ok: false; reason: string };

export interface PrecheckInput {
  candidate: FactorDef;
  /** 该计算任务已有的成员定义（含已停用）。 */
  members: FactorDef[];
  /** 源数据集的列名；还没加载出来时为 null，此时跳过依赖数据集的检查。 */
  sourceColumns: string[] | null;
}

/**
 * 「添加因子」弹窗的预检，只用于提前置灰与提示；权威校验永远是后端 AddFactorToSet（后端规格 §6.2 / D25）。
 */
export function precheckAddFactor({ candidate, members, sourceColumns }: PrecheckInput): PrecheckResult {
  if (members.some(item => item.factor_id === candidate.factor_id)) return { ok: false, reason: "已在该计算任务中" };
  if (!sourceColumns) return { ok: true };

  const source = new Set(sourceColumns);
  const missing = (candidate.input_columns || []).find(column => !source.has(column));
  if (missing) return { ok: false, reason: `输入列 ${missing} 不在源数据集中` };

  const owners = new Map<string, string>();
  for (const member of members) for (const output of member.outputs || []) owners.set(output, member.factor_id);
  for (const output of candidate.outputs || []) {
    if (source.has(output)) return { ok: false, reason: `输出列 ${output} 与源数据集列重名` };
    const owner = owners.get(output);
    if (owner) return { ok: false, reason: `输出列 ${output} 与因子 ${owner} 的输出列重名` };
  }
  return { ok: true };
}
