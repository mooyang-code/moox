import type { FactorDef, FactorMember, MemberKind } from "@/api/factor/types";
import { columns as memberColumns, fixedKey, varKey } from "@/utils/factor-name";

/** fixable 为真表示改一下参数或成员类型就可能通过（行保持可选）；否则是这个因子本身加不进去。 */
export type PrecheckResult = { ok: true; memberKey: string; columns: string[] } | { ok: false; reason: string; fixable?: true };

const fixable = (reason: string): PrecheckResult => ({ ok: false, reason, fixable: true });

/** 因子任务里已有成员中预检用到的部分。 */
export type PrecheckMember = Pick<FactorMember, "factor_id" | "member_key" | "kind"> & {
  params?: Record<string, number>;
  /** 成员在结果数据集里的列名（含成员键前缀）。 */
  output_columns: string[];
};

export interface PrecheckInput {
  candidate: FactorDef;
  /** 该因子任务已有的成员（含已停用）。 */
  members: PrecheckMember[];
  /** 源数据集的列名；还没加载出来时为 null，此时跳过依赖数据集的检查。 */
  sourceColumns: string[] | null;
  /** 成员类型，缺省为固定成员。 */
  kind?: MemberKind;
  /** 固定成员的固定参数、或可变成员的默认参数（没给的取定义的默认值）。 */
  params?: Record<string, number>;
}

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}

/**
 * 「添加因子」弹窗的预检，只用于提前置灰与提示；权威校验永远是后端 AddTaskMember。
 * 成员键与列名按 utils/factor-name 的规则推导（与服务端一致）：固定成员参数全为默认值时是因子 ID，否则是 因子ID__值1_值2…；
 * 可变成员是 因子ID__var，每个因子在一个任务里最多一个。
 */
export function precheckAddFactor({ candidate, members, sourceColumns, kind = "fixed", params }: PrecheckInput): PrecheckResult {
  const decls = candidate.params ?? [];
  for (const decl of decls) {
    const value = params?.[decl.name] ?? decl.default_value;
    if (!Number.isFinite(value) || value < decl.min || value > decl.max) {
      return fixable(`参数 ${decl.name} 需在 ${decl.min} ~ ${decl.max} 之间`);
    }
    if (decl.type === "int" && !Number.isInteger(value)) return fixable(`参数 ${decl.name} 必须是整数`);
  }
  let memberKey: string;
  let columns: string[];
  try {
    memberKey =
      kind === "var"
        ? varKey(candidate.factor_id)
        : fixedKey(
            candidate.factor_id,
            decls.map(decl => ({ name: decl.name, default: decl.default_value })),
            params
          );
    columns = memberColumns(candidate.factor_id, memberKey, candidate.outputs || []);
  } catch (error) {
    // 参数过长导致成员键超长时换一组参数可能通过；其余命名问题是因子定义本身的，加不进去。
    return kind === "fixed" && decls.length ? fixable(errorMessage(error)) : { ok: false, reason: errorMessage(error) };
  }
  const sameKey = members.find(item => item.member_key === memberKey);
  if (sameKey) {
    if (kind === "var") return { ok: false, reason: "已有这个因子的可变成员" };
    return decls.length ? fixable(`已有相同参数的成员 ${memberKey}`) : { ok: false, reason: "已在该计算任务中" };
  }
  if (!sourceColumns) return { ok: true, memberKey, columns };

  const source = new Set(sourceColumns);
  const missing = (candidate.input_columns || []).find(column => !source.has(column));
  if (missing) return { ok: false, reason: `输入列 ${missing} 不在源数据集中` };

  const owners = new Map<string, string>();
  for (const member of members) for (const column of member.output_columns) owners.set(column, member.member_key);
  for (const column of columns) {
    if (source.has(column)) return fixable(`输出列 ${column} 与源数据集列重名`);
    const owner = owners.get(column);
    if (owner) return fixable(`输出列 ${column} 与成员 ${owner} 的输出列重名`);
  }
  return { ok: true, memberKey, columns };
}
