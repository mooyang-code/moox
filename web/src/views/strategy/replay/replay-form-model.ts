/** 回放表单的状态：来源三选一（实例会话 / 已保存定义 / DSL 草稿），其余为 View、区间与手续费。 */
export interface ReplayFormState {
  source: "definition" | "dsl";
  strategy_id: string;
  dsl_yaml: string;
  instance_id: string;
  factor_task_id: string;
  start: string;
  end: string;
  fee_bps: number;
}

/** 回放对象三选一：实例会话固化的版本、已保存的定义，或未保存的 DSL 文本；都没选返回 null。 */
export function replaySourceOf(
  form: ReplayFormState
): { instance_id: string } | { strategy_id: string } | { dsl_yaml: string } | null {
  if (form.instance_id) return { instance_id: form.instance_id };
  if (form.source === "dsl") return form.dsl_yaml.trim() ? { dsl_yaml: form.dsl_yaml } : null;
  return form.strategy_id ? { strategy_id: form.strategy_id } : null;
}

/**
 * 发起前的本地校验，返回第一条错误（没有则为空串）：来源与因子任务、时间格式、结束晚于开始、手续费范围。
 * parse 把 YYYY-MM-DD HH:mm（UTC）转成 ISO 时间，格式不对返回 null。
 */
export function validateReplayForm(form: ReplayFormState, parse: (value: string) => string | null): string {
  if (!form.factor_task_id || !replaySourceOf(form)) return "请选择因子任务，并选择定义、粘贴 DSL 文本或从实例详情进入";
  const start = parse(form.start);
  const end = parse(form.end);
  if (!start || !end) return "时间格式应为 YYYY-MM-DD HH:mm（UTC），例如 2026-09-01 00:00";
  if (Date.parse(end) <= Date.parse(start)) return "结束时间必须晚于开始时间";
  const fee = Number(form.fee_bps);
  if (!Number.isFinite(fee) || fee < 0 || fee > 1000) return "单边手续费需在 0～1000 bps 之间";
  return "";
}
