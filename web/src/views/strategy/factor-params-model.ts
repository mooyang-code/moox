import type { ViewColumn } from "@/api/storage/types";
import type { DisplayHidden, ParamSubmission, ParamSubmissionStatus } from "@/api/strategy-types";

/** 实例启用时固化的私有因子集绑定；没有声明 factors 的实例返回 null。 */
export interface FactorBinding {
  /** 实例的私有因子集 ID（也是参数提交与冻结记录的集合）。 */
  setId: string;
  /** 事件路由与求值用的结果 View（私有因子集的结果 View）。 */
  viewId: string;
  /** 实例绑定的 K 线 View。 */
  sourceViewId: string;
  /** 声明的成员别名（排序）。 */
  members: string[];
  /** 各成员可调参数的名称（声明顺序）。 */
  params: Record<string, string[]>;
}

function parseObject(raw?: string): Record<string, any> {
  try {
    const value = JSON.parse(raw || "{}");
    return value && typeof value === "object" ? value : {};
  } catch {
    return {};
  }
}

export function parseFactorBinding(resolvedJson?: string): FactorBinding | null {
  const value = parseObject(resolvedJson);
  if (!value.factor_set_id) return null;
  const declared = (value.factor_params ?? {}) as Record<string, Array<{ name?: string }>>;
  const params: Record<string, string[]> = {};
  for (const [alias, list] of Object.entries(declared))
    params[alias] = (list ?? []).map(item => item?.name ?? "").filter(Boolean);
  const members = new Set(Object.keys(declared));
  for (const binding of Object.values((value.columns ?? {}) as Record<string, { member?: string }>)) {
    if (binding?.member) members.add(binding.member);
  }
  return {
    setId: value.factor_set_id,
    viewId: value.view_id ?? "",
    sourceViewId: value.source_view_id ?? "",
    members: [...members].sort(),
    params
  };
}

export const SUBMISSION_STATUS: Record<ParamSubmissionStatus, { label: string; color: string; hint: string }> = {
  pending: { label: "待提交", color: "arcoblue", hint: "已落库，等待提交给因子管理端" },
  accepted: { label: "已接受", color: "green", hint: "因子管理端已接受；是否被该期采用以冻结记录为准" },
  too_late: { label: "已晚到", color: "orange", hint: "该期参数已冻结，这份没有被采用（因子沿用上一期）" },
  rejected: { label: "被拒绝", color: "red", hint: "因子管理端拒绝了这份参数（范围、类型或成员不合法）" },
  abandoned: { label: "已放弃", color: "gray", hint: "实例停用、换了会话或超过重试期限，没有再提交" }
};

export function submissionStatus(status: string) {
  return SUBMISSION_STATUS[status as ParamSubmissionStatus] ?? { label: status, color: "gray", hint: "" };
}

/** 一份提交快照的摘要：成员 → 参数=统一值（覆盖个数），按成员与参数名排序。 */
export function submissionSummary(paramsJson: string): string {
  const members = parseObject(paramsJson) as Record<
    string,
    Record<string, { default?: number; overrides?: Record<string, number> }>
  >;
  return Object.entries(members)
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([alias, params]) => {
      const text = Object.entries(params ?? {})
        .sort(([a], [b]) => a.localeCompare(b))
        .map(([name, value]) => {
          const overrides = Object.keys(value?.overrides ?? {}).length;
          return `${name}=${value?.default ?? "-"}${overrides ? `（${overrides} 个标的覆盖）` : ""}`;
        })
        .join("，");
      return `${alias}: ${text}`;
    })
    .join("；");
}

export interface SubmissionRow extends Pick<ParamSubmission, "request_id" | "status" | "attempts" | "notes"> {
  periodText: string;
  statusLabel: string;
  statusColor: string;
  hint: string;
  summary: string;
  outcome: string;
}

function periodLabel(ms: number) {
  return ms > 0 ? new Date(ms).toISOString().replace("T", " ").slice(0, 19) + " UTC" : "-";
}

/** 逐成员结果文本（只列没有被接受的）：[{member, result, reason}]。 */
function outcomeText(outcomeJson: string) {
  let list: Array<{ member?: string; result?: string; reason?: string }> = [];
  try {
    const parsed = JSON.parse(outcomeJson || "[]");
    if (Array.isArray(parsed)) list = parsed;
  } catch {
    list = [];
  }
  return list
    .filter(item => item.result && item.result !== "accepted")
    .map(item => `${item.member ? `${item.member} ` : ""}${item.result}${item.reason ? `：${item.reason}` : ""}`)
    .join("；");
}

export function submissionRows(submissions: ParamSubmission[]): SubmissionRow[] {
  return submissions.map(item => {
    const status = submissionStatus(item.status);
    return {
      request_id: item.request_id,
      status: item.status,
      attempts: item.attempts,
      notes: item.notes,
      periodText: periodLabel(item.period_time),
      statusLabel: status.label,
      statusColor: status.color,
      hint: status.hint,
      summary: submissionSummary(item.params_json),
      outcome: outcomeText(item.outcome_json)
    };
  });
}

/** 把冻结记录的秒级时间转成与提交记录一致的文本。 */
export function freezePeriodText(seconds: number | string) {
  return periodLabel(Number(seconds) * 1000);
}

/**
 * 按外显设置算出要隐藏的列名：列的分类（category_id）、成员（member_alias）或列名在隐藏清单里就隐藏；
 * 参数列带所属成员的属性，随成员与分类一起显示或隐藏。只影响展示，不影响 View 的列和策略读到的数据。
 */
export function hiddenColumnNames(columns: ViewColumn[], hidden: DisplayHidden): string[] {
  const categories = new Set(hidden.categories);
  const members = new Set(hidden.members);
  const names = new Set(hidden.columns);
  return columns
    .filter(column => {
      const attributes = column.attributes ?? {};
      return (
        names.has(column.column_name) ||
        (attributes.member_alias && members.has(attributes.member_alias)) ||
        (attributes.category_id && categories.has(attributes.category_id))
      );
    })
    .map(column => column.column_name);
}
