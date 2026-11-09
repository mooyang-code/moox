import type { Strategy } from "@/api/strategy-types";
import type { FactorTask } from "@/api/factor/types";
import { TASK_KEY_ORDER, marketRequirement } from "@/views/strategy/market-mode";
import { freqSeconds } from "@/views/factor/shared/health";
import { addDays, msToWallClock, wallClockToMs } from "@/utils/time-zone";

/** 回放表单的状态：来源三选一（实例会话 / 已保存定义 / DSL 草稿），其余为各市场的计算任务、区间与两个手续费。 */
export interface ReplayFormState {
  source: "definition" | "dsl";
  strategy_id: string;
  dsl_yaml: string;
  instance_id: string;
  /** 策略没写 market 时只读一个任务，市场由用户选定。 */
  single_market: string;
  /** 市场 → 计算任务；从实例发起时预填实例关联的任务。 */
  tasks: Record<string, string>;
  /** 回放区间 [起, 止)，毫秒时刻；在页面里按空间时区选择 */
  range: [number, number] | null;
  /** 现货与股票的单边手续费、永续的单边手续费（bps）。 */
  spot_fee_bps: number;
  swap_fee_bps: number;
  /** 永续持仓结算资金费用的列（例如 funding_rate）；空串表示不计。 */
  funding_column: string;
}

/** 回放对象三选一：实例会话固化的版本、已保存的定义，或未保存的 DSL 文本；都没选返回 null。 */
export function replaySourceOf(
  form: ReplayFormState
): { instance_id: string } | { strategy_id: string } | { dsl_yaml: string } | null {
  if (form.instance_id) return { instance_id: form.instance_id };
  if (form.source === "dsl") return form.dsl_yaml.trim() ? { dsl_yaml: form.dsl_yaml } : null;
  return form.strategy_id ? { strategy_id: form.strategy_id } : null;
}

/** 回放用的 DSL 文本：草稿取输入框，已保存的定义取列表里的当前版本；从实例发起时没有（版本在实例会话里）。 */
export function replayDslText(form: ReplayFormState, strategies: Strategy[]): string {
  if (form.instance_id) return "";
  return form.source === "dsl" ? form.dsl_yaml : (strategies.find(item => item.strategy_id === form.strategy_id)?.dsl_yaml ?? "");
}

/**
 * 回放要读取任务的市场（现货、永续、股票先后，统一数据集上的任务键是 multi）：从实例发起时就是预填的实例任务的键；否则按策略 DSL 的 market 读出，
 * 没写 market 时只读一个任务，市场取用户选的。
 */
export function replayMarkets(form: ReplayFormState, strategies: Strategy[]): string[] {
  if (form.instance_id) return TASK_KEY_ORDER.filter(market => form.tasks[market]);
  const requirement = marketRequirement(replayDslText(form, strategies));
  if (requirement.declared) return requirement.markets;
  return form.single_market ? [form.single_market] : [];
}

/** 提交用的 factor_tasks：只含回放要读的市场；从实例发起且没有预填任务时为空，由后端读实例关联的任务。 */
export function replayFactorTasks(form: ReplayFormState, strategies: Strategy[]): Record<string, string> {
  return Object.fromEntries(replayMarkets(form, strategies).map(market => [market, form.tasks[market] ?? ""]));
}

/** 发起前的本地校验，返回第一条错误（没有则为空串）：来源与各市场的计算任务、区间、两个手续费范围。 */
export function validateReplayForm(form: ReplayFormState, strategies: Strategy[] = []): string {
  if (!replaySourceOf(form)) return "请选择定义、粘贴 DSL 文本或从实例详情进入";
  const markets = replayMarkets(form, strategies);
  // 从实例发起且入口没有带任务：不给 factor_tasks，后端读实例关联的任务。
  const omitted = Boolean(form.instance_id) && !markets.length;
  if (!omitted && !markets.length) return "请选择策略的计算任务（策略没写 market 时先选任务所属的市场）";
  if (markets.some(market => !form.tasks[market])) return "请为策略用到的每个市场选择计算任务";
  if (!form.range) return "请选择回放区间";
  if (form.range[1] <= form.range[0]) return "结束时间必须晚于开始时间";
  for (const fee of [Number(form.spot_fee_bps), Number(form.swap_fee_bps)]) {
    if (!Number.isFinite(fee) || fee < 0 || fee > 1000) return "单边手续费需在 0～1000 bps 之间";
  }
  const funding = replayFundingColumn(form, strategies);
  if (funding && !/^[A-Za-z_][A-Za-z0-9_]*$/.test(funding)) return "资金费列只能是 View 里的列名";
  return "";
}

/** 提交用的资金费列：只在回放读取永续（含统一数据集）时带上，表单里隐藏的旧值不提交；没写时为空串。 */
export function replayFundingColumn(form: ReplayFormState, strategies: Strategy[] = []): string {
  const markets = replayMarkets(form, strategies);
  if (!markets.includes("swap") && !markets.includes("multi")) return "";
  return (form.funding_column ?? "").trim();
}

/** 选的计算任务都是日线时区间只选日期（按空间时区的整天）；没选任务或有日内任务时选到分钟。 */
export function replayDateOnly(
  form: ReplayFormState,
  strategies: Strategy[],
  tasks: Pick<FactorTask, "task_id" | "freq">[]
): boolean {
  const chosen = Object.values(replayFactorTasks(form, strategies)).filter(Boolean);
  if (!chosen.length) return false;
  return chosen.every(id => freqSeconds(tasks.find(task => task.task_id === id)?.freq ?? "") === 86_400);
}

/**
 * 只选日期时把区间吸附成和选择器显示一致的整天（按空间时区）：起点取所在日期零点，终点取 range[1] − 1 所在日期的下一天零点。
 * 否则整点的默认区间提交后，后端只取开盘不早于起点的 bar，第一天会被跳过。
 */
export function wholeDays(range: [number, number], zone: string): [number, number] {
  const start = wallClockToMs(msToWallClock(range[0], zone, "date"), zone)!;
  const end = wallClockToMs(addDays(msToWallClock(range[1] - 1, zone, "date"), 1), zone)!;
  return [start, end];
}
