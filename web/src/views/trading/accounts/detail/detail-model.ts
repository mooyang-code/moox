import type { EquityPoint, LogicalAccountTarget, OperatorAction, TradingAccount } from "@/api/trade";
import type { PositionSnapshot } from "../shared/position-snapshot";

export const detailTabs = [
  { key: "overview", label: "概览" },
  { key: "positions", label: "持仓与目标" },
  { key: "orders", label: "订单" },
  { key: "fills", label: "成交" },
  { key: "ledger", label: "资金流水" },
  { key: "settings", label: "成员与设置" }
] as const;

export type DetailTab = (typeof detailTabs)[number]["key"];

export function tabFromQuery(value: unknown): DetailTab {
  return detailTabs.find(item => item.key === value)?.key ?? "overview";
}

function num(value: string | undefined): number {
  const n = Number(value);
  return Number.isFinite(n) ? n : 0;
}

export interface MemberTotals {
  equity: number | null;
  available: number | null;
  margin: number;
  unrealizedPnl: number;
}

/** 启用成员的资金合计。没有任何成员给出权益时，权益和可用资金为 null。 */
export function memberTotals(accounts: readonly TradingAccount[]): MemberTotals {
  // 后端总会带 snapshot，首次同步前里面的权益是空串：这种按没读到算，不能当成 0
  const withSnapshot = accounts.filter(item => item.snapshot?.equity);
  return {
    equity: withSnapshot.length ? withSnapshot.reduce((sum, item) => sum + num(item.snapshot?.equity), 0) : null,
    available: withSnapshot.length ? withSnapshot.reduce((sum, item) => sum + num(item.snapshot?.available_funds), 0) : null,
    margin: withSnapshot.reduce((sum, item) => sum + num(item.snapshot?.used_margin), 0),
    unrealizedPnl: withSnapshot.reduce((sum, item) => sum + num(item.snapshot?.unrealized_pnl), 0)
  };
}

/** 模拟盘的初始资金合计，较初始资金的盈亏用它算。 */
export function paperInitialBalance(accounts: readonly TradingAccount[]): number | null {
  const paper = accounts.filter(item => item.paper);
  return paper.length ? paper.reduce((sum, item) => sum + num(item.paper?.initial_balance), 0) : null;
}

/** 较初始资金的变化：还没读到权益（首次同步前）时为 null，不能把权益当 0 算成 −100%。 */
export function equityChangeOf(equity: number | null, initial: number | null): number | null {
  return equity === null || !initial ? null : equity - initial;
}

export interface EquityDelta {
  amount: number;
  ratio: number;
}

/** 一段时间内的权益变化：资金曲线第一个点到最后一个点。不足两个点或起点权益为 0 时返回 null。 */
export function equityDelta(points: readonly EquityPoint[]): EquityDelta | null {
  const sorted = [...points].sort((a, b) => Number(a.bucket_time) - Number(b.bucket_time));
  if (sorted.length < 2) return null;
  const first = Number(sorted[0].equity);
  const last = Number(sorted[sorted.length - 1].equity);
  if (!Number.isFinite(first) || !Number.isFinite(last) || first === 0) return null;
  return { amount: last - first, ratio: last / first - 1 };
}

/** 清仓预览：要平掉的持仓个数和价值（永续取标记价，现货取估值）。accountId 为空表示整个组合。 */
export function flattenPreview(snapshot: PositionSnapshot, accountId?: string) {
  const swaps = snapshot.swapPositions.filter(item => !accountId || item.trading_account_id === accountId);
  const spots = snapshot.spotHoldings.filter(
    item => item.instrument_id && Number(item.quantity) !== 0 && (!accountId || item.trading_account_id === accountId)
  );
  const value =
    swaps.reduce((sum, item) => sum + Math.abs(num(item.signed_quantity) * num(item.mark_price)), 0) +
    spots.reduce((sum, item) => sum + Math.abs(num(item.market_value)), 0);
  return { positions: swaps.length + spots.length, value };
}

export function targetExpired(target: LogicalAccountTarget | null, now = Date.now()): boolean {
  if (!target) return false;
  const validUntil = Number(target.valid_until);
  return target.status === "EXPIRED" || (Number.isFinite(validUntil) && validUntil > 0 && validUntil <= now);
}

/** 清仓动作进度：撤单和平仓各完成多少没有逐项数据，页头只显示动作状态和各执行账户的结果。 */
export function isFlattenRunning(action: OperatorAction | null): boolean {
  return Boolean(action && action.action_type === "FLATTEN" && action.status === "RUNNING");
}

/** 刷新页面后接回跟踪：最近的人工操作里还在进行中的那一个（记录按时间倒序）。 */
export function runningAction(actions: OperatorAction[] | undefined): OperatorAction | null {
  return (actions || []).find(action => action.status === "RUNNING") ?? null;
}

/** 订单状态的配色：订单列表和订单详情共用。 */
export function orderStateTone(state: string): "ok" | "bad" | "warn" | "run" | "" {
  switch (state) {
    case "FILLED":
      return "ok";
    case "REJECTED":
    case "EXPIRED":
      return "bad";
    case "PARTIALLY_FILLED":
    case "CANCELING":
    case "SUBMIT_UNKNOWN":
    case "CANCEL_UNKNOWN":
      return "warn";
    case "CANCELED":
    case "PARTIALLY_CANCELED":
      return "";
    default:
      return "run";
  }
}
