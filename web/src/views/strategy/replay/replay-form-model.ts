import type { ReplayInput, Strategy, StrategyTaskRequirement } from "@/api/strategy-types";
import type { FactorTask } from "@/api/factor/types";
import { MARKET_ORDER, requirementTaskKeys, taskKeyMarkets } from "@/views/strategy/market-mode";
import { parseDSL } from "@/views/strategy/dsl";
import { freqSeconds } from "@/views/factor/shared/health";
import { addDays, msToWallClock, wallClockToMs } from "@/utils/time-zone";

/** 回放表单的状态：来源三选一（实例会话 / 已保存定义 / DSL 草稿），其余为各市场的计算任务、因子数据来源、区间、两个手续费与滑点。 */
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
  /** 滑点（bps）：成交价在收盘价上按方向加减这个比例。 */
  slippage_bps: number;
  /** 永续持仓结算资金费用的列（例如 funding_rate）；空串表示不计。 */
  funding_column: string;
  /** 主帧的因子数据来源：view 读结果 View，compute 是因子现算。 */
  input: ReplayInput;
  /** 从实例发起时实例引用的策略定义（页面按实例 ID 读回）；只用来判断是不是策略池，没读到时为空。 */
  instance_strategy_id?: string;
  /** 后端按定义或 DSL 草稿推出的要绑定的任务（页面读回）；从实例发起或还没读到时为空。 */
  requirement?: StrategyTaskRequirement | null;
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

/** 回放对象是策略池时，后端推出的要绑定的任务；不是策略池、从实例发起或还没读到时返回 null。 */
export function replayPool(form: ReplayFormState): StrategyTaskRequirement | null {
  return !form.instance_id && form.requirement?.pool ? form.requirement : null;
}

/**
 * 回放对象是不是策略池：从实例发起时按实例引用的定义（当前版本）判断，实例的定义还没读到时按不是池处理；其余来源看 DSL 本身。
 * 只用来决定资金费列与说明（池的资金费写在池 DSL 里），不用来校验成员：实例回放的成员取会话快照，由后端校验。
 */
export function replayIsPool(form: ReplayFormState, strategies: Strategy[]): boolean {
  if (!form.instance_id) return Boolean(replayPool(form));
  const text = strategies.find(item => item.strategy_id === form.instance_strategy_id)?.dsl_yaml ?? "";
  return Boolean(form.instance_strategy_id && parseDSL(text).preview?.pool);
}

/**
 * 回放对象是不是声明了 factor_params 的策略（这种策略只能用因子现算来源回放）：从实例发起时按实例引用的定义（当前版本）判断，
 * 实例的定义还没读到时按没声明处理（由后端校验）；其余来源看 DSL 本身。
 */
export function replayTuned(form: ReplayFormState, strategies: Strategy[]): boolean {
  const text = form.instance_id
    ? (strategies.find(item => item.strategy_id === form.instance_strategy_id)?.dsl_yaml ?? "")
    : replayDslText(form, strategies);
  return Boolean(parseDSL(text).preview?.tuned);
}

/** 要读的市场是否由 DSL 决定：写了 market，或是策略池（成员市场的并集）；否则要先选任务所属的市场。 */
export function replayDeclared(form: ReplayFormState): boolean {
  return Boolean(form.requirement?.marketDeclared);
}

/**
 * 回放要读取任务的市场（现货、永续、股票先后）：从实例发起时就是预填的实例任务的键；其余按后端推出的要求，
 * 没写 market 时只读一个任务，市场取用户选的。
 */
export function replayMarkets(form: ReplayFormState): string[] {
  if (form.instance_id) return MARKET_ORDER.filter(market => form.tasks[market]);
  return taskKeyMarkets(requirementTaskKeys(form.requirement, form.single_market));
}

/** drift 池的回放起点说明：写了漂移起点时起点必须选它，没写时漂移从区间的第一根开始；不是 drift 池时为空串。 */
export function replayDriftHint(form: ReplayFormState, strategies: Strategy[]): string {
  const pool = parseDSL(replayDslText(form, strategies)).preview?.pool;
  if (pool?.method !== "drift") return "";
  return pool.driftStart
    ? `drift 池：起点必须选漂移起点 ${pool.driftStart}，结果才与实时可比。`
    : "drift 池：漂移从区间的第一根开始，与实时不可比；要逐期比对请在池 DSL 写 allocation.start。";
}

/** 提交用的 factor_tasks：只含回放要读的市场；从实例发起且没有预填任务时为空，由后端读实例关联的任务。 */
export function replayFactorTasks(form: ReplayFormState): Record<string, string> {
  return Object.fromEntries(replayMarkets(form).map(market => [market, form.tasks[market] ?? ""]));
}

/** 发起前的本地校验，返回第一条错误（没有则为空串）：来源与各市场的计算任务、区间、两个手续费与滑点的范围。 */
export function validateReplayForm(form: ReplayFormState, strategies: Strategy[] = []): string {
  if (!replaySourceOf(form)) return "请选择定义、粘贴 DSL 文本或从实例详情进入";
  const problems = form.instance_id ? [] : (form.requirement?.problems ?? []);
  if (problems.length) return `这份定义还不能回放：${problems.join("；")}`;
  const markets = replayMarkets(form);
  // 从实例发起且入口没有带任务：不给 factor_tasks，后端读实例关联的任务。
  const omitted = Boolean(form.instance_id) && !markets.length;
  if (!omitted && !markets.length) return "请选择策略的计算任务（策略没写 market 时先选任务所属的市场）";
  if (markets.some(market => !form.tasks[market])) return "请为策略用到的每个市场选择计算任务";
  const inputError = replayInputError(form, strategies, markets);
  if (inputError) return inputError;
  if (!form.range) return "请选择回放区间";
  if (form.range[1] <= form.range[0]) return "结束时间必须晚于开始时间";
  for (const fee of [Number(form.spot_fee_bps), Number(form.swap_fee_bps)]) {
    if (!Number.isFinite(fee) || fee < 0 || fee > 1000) return "单边手续费需在 0～1000 bps 之间";
  }
  const slippage = Number(form.slippage_bps);
  if (!Number.isFinite(slippage) || slippage < 0 || slippage > 200) return "滑点需在 0～200 bps 之间";
  const funding = replayFundingColumn(form, strategies);
  if (funding && !/^[A-Za-z_][A-Za-z0-9_]*$/.test(funding)) return "资金费列只能是 View 里的列名";
  return "";
}

/** 因子数据来源的本地校验（与后端一致）：声明了 factor_params 的策略必须用因子现算；因子现算不支持策略池、两个计算任务与股票。 */
function replayInputError(form: ReplayFormState, strategies: Strategy[], markets: string[]): string {
  if (form.input !== "compute") {
    return replayTuned(form, strategies) ? "声明了 factor_params 的策略只能用“因子现算”来源回放" : "";
  }
  if (replayIsPool(form, strategies)) return "“因子现算”来源不支持策略池";
  if (markets.length > 1) return "“因子现算”只支持一个计算任务：现货加永续请改用结果 View";
  if (markets.includes("equity")) return "“因子现算”暂不支持股票市场";
  return "";
}

/** 提交用的资金费列：只在回放读取永续时带上，表单里隐藏的旧值不提交；策略池的资金费写在池 DSL 里，不提交；没写时为空串。 */
export function replayFundingColumn(form: ReplayFormState, strategies: Strategy[] = []): string {
  if (replayIsPool(form, strategies)) return "";
  const markets = replayMarkets(form);
  if (!markets.includes("swap")) return "";
  return (form.funding_column ?? "").trim();
}

/** 选的计算任务都是日线时区间只选日期（按空间时区的整天）；没选任务或有日内任务时选到分钟。 */
export function replayDateOnly(form: ReplayFormState, tasks: Pick<FactorTask, "task_id" | "freq">[]): boolean {
  const chosen = Object.values(replayFactorTasks(form)).filter(Boolean);
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
