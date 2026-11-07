import type { Strategy } from "@/api/strategy-types";
import { MARKET_ORDER, marketRequirement } from "@/views/strategy/market-mode";

/** 回放表单的状态：来源三选一（实例会话 / 已保存定义 / DSL 草稿），其余为各市场的因子任务、区间与两个手续费。 */
export interface ReplayFormState {
  source: "definition" | "dsl";
  strategy_id: string;
  dsl_yaml: string;
  instance_id: string;
  /** 策略没写 market 时只读一个任务，市场由用户选定。 */
  single_market: string;
  /** 市场 → 因子任务；从实例发起时预填实例关联的任务。 */
  tasks: Record<string, string>;
  start: string;
  end: string;
  /** 现货与股票的单边手续费、永续的单边手续费（bps）。 */
  spot_fee_bps: number;
  swap_fee_bps: number;
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
 * 回放要读取任务的市场（现货、永续、股票先后）：从实例发起时就是预填的实例任务的市场；否则按策略 DSL 的 market 读出，
 * 没写 market 时只读一个任务，市场取用户选的。
 */
export function replayMarkets(form: ReplayFormState, strategies: Strategy[]): string[] {
  if (form.instance_id) return MARKET_ORDER.filter(market => form.tasks[market]);
  const requirement = marketRequirement(replayDslText(form, strategies));
  if (requirement.declared) return requirement.markets;
  return form.single_market ? [form.single_market] : [];
}

/** 提交用的 factor_tasks：只含回放要读的市场。 */
export function replayFactorTasks(form: ReplayFormState, strategies: Strategy[]): Record<string, string> {
  return Object.fromEntries(replayMarkets(form, strategies).map(market => [market, form.tasks[market] ?? ""]));
}

/**
 * 发起前的本地校验，返回第一条错误（没有则为空串）：来源与各市场的因子任务、时间格式、结束晚于开始、两个手续费范围。
 * parse 把 YYYY-MM-DD HH:mm（UTC）转成 ISO 时间，格式不对返回 null。
 */
export function validateReplayForm(
  form: ReplayFormState,
  parse: (value: string) => string | null,
  strategies: Strategy[] = []
): string {
  if (!replaySourceOf(form)) return "请选择定义、粘贴 DSL 文本或从实例详情进入";
  const markets = replayMarkets(form, strategies);
  if (!markets.length) return "请选择策略的因子任务（策略没写 market 时先选任务所属的市场）";
  if (markets.some(market => !form.tasks[market])) return "请为策略用到的每个市场选择因子任务";
  const start = parse(form.start);
  const end = parse(form.end);
  if (!start || !end) return "时间格式应为 YYYY-MM-DD HH:mm（UTC），例如 2026-09-01 00:00";
  if (Date.parse(end) <= Date.parse(start)) return "结束时间必须晚于开始时间";
  for (const fee of [Number(form.spot_fee_bps), Number(form.swap_fee_bps)]) {
    if (!Number.isFinite(fee) || fee < 0 || fee > 1000) return "单边手续费需在 0～1000 bps 之间";
  }
  return "";
}
