import type { StrategyResult } from "@/api/strategy-types";

/** 实例创建时选定的因子任务及其可变成员（来自启用时固化的解析结果）；解析结果里没有任务时返回 null。 */
export interface FactorBinding {
  /** 实例关联的因子任务 ID（也是参数提交的目标）。 */
  taskId: string;
  /** 事件路由与求值用的结果 View（任务的结果 View）。 */
  viewId: string;
  /** 策略逐期提交参数的可变成员（成员键，排序）。 */
  members: string[];
  /** 各可变成员可调参数的名称（声明顺序）。 */
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
  if (!value.factor_task_id) return null;
  const declared = (value.factor_params ?? {}) as Record<string, Array<{ name?: string }>>;
  const params: Record<string, string[]> = {};
  for (const [member, list] of Object.entries(declared))
    params[member] = (list ?? []).map(item => item?.name ?? "").filter(Boolean);
  return { taskId: value.factor_task_id, viewId: value.view_id ?? "", members: Object.keys(declared).sort(), params };
}

/** 一个参数取值的摘要文本：统一值，有按标的覆盖时附上个数。 */
function valueText(value?: { default?: number; default_value?: number; overrides?: Record<string, number> }) {
  const overrides = Object.keys(value?.overrides ?? {}).length;
  const base = value?.default ?? value?.default_value ?? "-";
  return `${base}${overrides ? `（${overrides} 个标的覆盖）` : ""}`;
}

/** 一份成员参数的摘要：成员 → 参数=统一值（覆盖个数），按成员与参数名排序。 */
export function memberParamsSummary(members: Record<string, Record<string, any>>): string {
  return Object.entries(members)
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([member, params]) => {
      const text = Object.entries(params ?? {})
        .sort(([a], [b]) => a.localeCompare(b))
        .map(([name, value]) => `${name}=${valueText(value)}`)
        .join("，");
      return `${member}: ${text}`;
    })
    .join("；");
}

/** 结果上的参数投递状态：已送达、被因子侧拒绝（不再重试）、待送达。 */
export function deliveryStatus(result: Pick<StrategyResult, "params_delivered_at" | "params_error">) {
  if (result.params_delivered_at) return { key: "delivered", label: "已送达", color: "green", hint: "因子管理端已接受" };
  if (result.params_error) return { key: "rejected", label: "被拒绝", color: "red", hint: result.params_error };
  return {
    key: "pending",
    label: "待送达",
    color: "arcoblue",
    hint: "已随结果落库，等待提交给因子管理端；只投当前会话里最新的一条"
  };
}

export interface NextParamsRow {
  resultId: string;
  /** 产出参数的结果所在的周期（bar_end_time）。 */
  barEnd: string;
  /** 参数生效的目标期（Unix 秒的文本化时间）。 */
  targetText: string;
  summary: string;
  statusKey: string;
  statusLabel: string;
  statusColor: string;
  hint: string;
}

function periodLabel(seconds: number) {
  return seconds > 0 ? new Date(seconds * 1000).toISOString().replace("T", " ").slice(0, 19) + " UTC" : "-";
}

/** 把结果里的 next_params_json（{period, members}）整理成表格行；没有可变成员参数的结果略过，新的在前。 */
export function nextParamsRows(results: StrategyResult[]): NextParamsRow[] {
  const rows: NextParamsRow[] = [];
  for (const result of results) {
    if (!result.next_params_json) continue;
    const envelope = parseObject(result.next_params_json) as {
      period?: number | string;
      members?: Record<string, Record<string, any>>;
    };
    const status = deliveryStatus(result);
    rows.push({
      resultId: result.result_id,
      barEnd: result.bar_end_time,
      targetText: periodLabel(Number(envelope.period ?? 0)),
      summary: memberParamsSummary(envelope.members ?? {}),
      statusKey: status.key,
      statusLabel: status.label,
      statusColor: status.color,
      hint: status.hint
    });
  }
  return rows.sort((a, b) => b.barEnd.localeCompare(a.barEnd));
}

/** 把 Unix 秒转成与上面一致的文本。 */
export function periodText(seconds: number | string) {
  return periodLabel(Number(seconds));
}
