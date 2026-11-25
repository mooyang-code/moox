import type {
  DelistExchange,
  DelistMarket,
  DelistRecord,
  InstrumentHolder,
  RegisterDelistNoticeReq,
  SkippedAccount
} from "@/api/trade/delist";
import { NETWORK_ERROR_MESSAGE, isNetworkFailure } from "@/api/network-error";

export interface HolderSummary {
  /** 实盘组合（测试网和生产环境，持有者里分不出这两种）：会被真实平仓，需要二次确认。 */
  live: InstrumentHolder[];
  paper: InstrumentHolder[];
  dust: InstrumentHolder[];
}

/** 预览结果分组：零头不会被平仓，单独列出；其余按是否模拟盘分开。 */
export function summarizeHolders(holders: InstrumentHolder[]): HolderSummary {
  const summary: HolderSummary = { live: [], paper: [], dust: [] };
  for (const holder of holders) {
    if (holder.dust) summary.dust.push(holder);
    else if (holder.paper) summary.paper.push(holder);
    else summary.live.push(holder);
  }
  return summary;
}

const HOLDER_STATUS_TEXT: Record<string, string> = {
  DISABLED: "账户已停用，平仓会失败，需要人工处理",
  ERROR: "账户出错，平仓可能失败，需要人工处理"
};

/** 持有者一行：数量之外，停用或出错的账户和 Trade 的标注也要写出来，这是破坏性操作的确认依据。 */
export function holderLine(holder: InstrumentHolder): string {
  const extras = [HOLDER_STATUS_TEXT[holder.status], holder.note].filter(Boolean);
  const tail = extras.length ? `（${extras.join("；")}）` : "";
  return `${holder.space_id} / ${holder.logical_account_id}（执行账户 ${holder.trading_account_id}）数量 ${holder.quantity}${tail}`;
}

/** 没能判断的账户一行。 */
export function skippedLine(account: SkippedAccount): string {
  return `执行账户 ${account.trading_account_id}：${account.reason || "原因未知"}`;
}

/**
 * 登记前二次确认的勾选框文字；不需要确认时为空。几种情况同时出现时合并成一句，每件要确认的事都写出来：
 * “会平仓实盘组合”是破坏性操作的确认依据，不能因为同时有没能判断的账户就被吞掉。
 */
export function confirmCheckboxText(input: {
  previewFailed: boolean;
  verified: boolean;
  skipped: number;
  live: number;
  willProcess: boolean;
}): string {
  if (input.previewFailed || !input.verified) return "我知道无法确认持有情况，仍然登记";
  const parts: string[] = [];
  if (input.live > 0) parts.push(input.willProcess ? "有实盘组合会被平仓" : "有实盘组合持有这个标的");
  if (input.skipped > 0) parts.push("有账户没能判断持有情况");
  if (!parts.length) return "";
  return `我确认${parts.join("，并且")}，仍然登记`;
}

/** 手工登记时可能出现的取值。 */
export const DELIST_EXCHANGES = [
  { value: "binance", label: "币安" },
  { value: "okx", label: "OKX" }
] as const;

export const DELIST_MARKETS = [
  { value: "spot", label: "现货" },
  { value: "swap", label: "永续合约" }
] as const;

/** 手工登记允许补录已经过去的时间，所以只检查有没有填。 */
export function delistTimeError(delistAtMs: number | null): string {
  if (!delistAtMs || !Number.isFinite(delistAtMs)) return "请选择下架时间";
  return "";
}

/** 表单里填的值（交易所代码、下架时间都还没规整）。 */
export interface DelistFormValues {
  exchange: DelistExchange;
  market: DelistMarket;
  exchange_symbol: string;
  /** 毫秒时刻（按 UTC 选择）；没选时为 null */
  delist_at: number | null;
  paper_only: boolean;
}

/** 点“下一步”时存下的登记参数：确认页的标题、提示和最终提交都只用它，预览之后再改表单也不会让两边对不上。 */
export interface DelistSnapshot extends RegisterDelistNoticeReq {
  /** 预览时下架时间是否已经过去。 */
  past: boolean;
}

/** 过去的下架时间：Trade 直接等核对，不再下单。 */
export function isPastDelistTime(delistAtMs: number, nowMs: number): boolean {
  return delistAtMs <= nowMs;
}

/** 把表单规整成登记参数（代码去空白、转大写，时间换成毫秒）；缺项时返回提示文字。 */
export function delistSnapshot(form: DelistFormValues, nowMs: number): { snapshot: DelistSnapshot } | { error: string } {
  const symbol = form.exchange_symbol.trim().toUpperCase();
  if (!symbol) return { error: "请填写交易所代码" };
  const delistAtMs = form.delist_at;
  const timeError = delistTimeError(delistAtMs);
  if (timeError || delistAtMs === null) return { error: timeError };
  return {
    snapshot: {
      exchange: form.exchange,
      market: form.market,
      exchange_symbol: symbol,
      delist_at_ms: delistAtMs,
      paper_only: form.paper_only,
      past: isPastDelistTime(delistAtMs, nowMs)
    }
  };
}

/** 只认最后一次发出的请求：每次发请求前取一个序号，响应回来时序号不是最新的就丢掉（对话框关掉重开也会换号）。 */
export function createLatestGuard() {
  let current = 0;
  return {
    next: () => ++current,
    isLatest: (ticket: number) => ticket === current,
    reset: () => {
      current++;
    }
  };
}

/** “仅模拟盘”的说明：演练的禁止只对模拟盘生效。 */
export const PAPER_ONLY_NOTE =
  "演练只对模拟盘生效：模拟盘组合禁止买入这个标的并按时间线平仓，实盘组合照常买卖、持仓不受影响；演练核对结束后名单里这一项自动删掉。";

/** 下架时间已经过去时的说明：仍会写下架名单；真实下架结束后名单保留，演练结束后删掉。 */
export function pastTimeNote(paperOnly: boolean): string {
  const tail = paperOnly ? "演练核对结束后删掉。" : "真实下架核对结束后名单保留、不会解除。";
  return `这个时间已经过去，不会下单，只在下架后 10 分钟核对；仍会写下架名单（禁止买入），${tail}`;
}

/** 下架时间输入框下的说明。 */
export const DELIST_TIME_HINT =
  "按 UTC 填写（交易所公告的下架时间一般也给 UTC）。填已经过去的时间时不会下单，只做核对；仍会写下架名单（禁止买入），真实下架（不打开“仅模拟盘”）结束后名单保留、不会解除。";

/** 请求失败的原因文字：没拿到响应、超时换成中文说明，服务端返回的错误原样显示。 */
export function requestErrorText(error: unknown): string {
  if (isNetworkFailure(error)) return NETWORK_ERROR_MESSAGE;
  const failure = error as { code?: unknown; message?: unknown } | null;
  if (failure?.code === "ECONNABORTED" || failure?.code === "ETIMEDOUT") return "请求超时，交易服务没有及时响应";
  return typeof failure?.message === "string" && failure.message ? failure.message : "未知原因";
}

/** 名单与下架记录的范围。 */
export function scopeLabel(scope: string): string {
  return scope === "PAPER_ONLY" ? "仅模拟盘" : "全部";
}

const DELIST_STATUS_TEXT: Record<string, string> = {
  ACTIVE: "进行中",
  COMPLETED: "已完成",
  FAILED: "核对失败",
  CANCELED: "已取消",
  RESCHEDULED: "已改期，由新记录接着处理"
};

export function delistStatusText(status: string): string {
  return DELIST_STATUS_TEXT[status] ?? status;
}

export function delistStatusColor(status: string): string {
  return { ACTIVE: "arcoblue", COMPLETED: "green", FAILED: "red" }[status] ?? "gray";
}

/** 已规划到第几轮。 */
export function plannedRoundText(round: number): string {
  return ["还没到下架前 24 小时", "已按下架前 24 小时规划", "已按下架前 2 小时复查"][round] ?? `第 ${round} 轮`;
}

const ACTION_STATUS_TEXT: Record<string, string> = {
  RUNNING: "运行中",
  COMPLETED: "已完成",
  PARTIAL: "只平了一部分",
  FAILED: "失败"
};

/** 一轮一个组合的动作状态；空表示还没发起（组合正忙或解析不到标的，下一次推进再试）。 */
export function actionStatusText(status: string): string {
  if (!status) return "还没发起";
  return ACTION_STATUS_TEXT[status] ?? status;
}

/** 页上段列进行中的记录与最近结束的若干条：进行中的排前面，其余按创建时间倒序。 */
export function sortDelists(records: DelistRecord[]): DelistRecord[] {
  return [...records].sort((a, b) => {
    const activeA = a.status === "ACTIVE" ? 0 : 1;
    const activeB = b.status === "ACTIVE" ? 0 : 1;
    if (activeA !== activeB) return activeA - activeB;
    return b.created_at_ms - a.created_at_ms;
  });
}
