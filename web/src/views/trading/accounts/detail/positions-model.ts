import type { OrderSettings } from "@/api/trade/settings";
import type { BlockedTarget, Holding, InstrumentTarget, Position } from "@/api/trade/types";
import { coinOf, marketOfInstrument } from "../shared/markets";
import { decimalAdd, decimalSub } from "../shared/order-rules";

export interface TargetCell {
  /** 目标数量；保持当前持仓时为 null */
  quantity: number | null;
  /** 目标数量的原始小数字符串，算差额用 */
  text: string;
  hold: boolean;
}

export interface CoinRow {
  coin: string;
  targetSpot?: TargetCell;
  targetSwap?: TargetCell;
  actualSpot: number;
  actualSwap: number;
  /** 实际数量的精确小数字符串（与 actualSpot、actualSwap 同口径），算差额用 */
  actualSpotText: string;
  actualSwapText: string;
  /** 按执行账户分开的实际数量（键是执行账户 ID），行内下单默认选持仓最大的账户用 */
  memberSpot: Record<string, number>;
  memberSwap: Record<string, number>;
  /** 实际减目标；这个市场没有目标或保持当前持仓时为 null */
  deviationSpot: number | null;
  deviationSwap: number | null;
  /** 差额的精确小数字符串，行内下单预填数量用，不经过浮点 */
  deviationSpotText: string | null;
  deviationSwapText: string | null;
  /** 现货钱包里这个币的总数，包含不受管理的部分 */
  spotBalance: number;
  /** 股票：可卖数量（扣掉冻结和当日买入）；股票行的目标、实际、偏差记在 Spot 那一列 */
  sellable: number;
  stock: boolean;
  /** 未执行的原因，没有则为空 */
  blockedReason: string;
  /** 被单币每小时上限顺延到下一个整点的说明，没有则为空：不算未执行，状态仍按差额判断 */
  deferredReason: string;
  /** 标记价，按组合权益比例的阈值要用；取不到为 0 */
  markSpot: number;
  markSwap: number;
  /** 这个市场的差额执行器还会下单（目标已收敛时都为假）；任一为真时整行算未到位，偏差各格按它着色 */
  offSpot: boolean;
  offSwap: boolean;
  state: "ok" | "off" | "blocked" | "extra";
  /** 相对偏差的最大值，用于排序 */
  worstRatio: number;
}

export interface BuildCoinRowsInput {
  targets: readonly InstrumentTarget[];
  blocked: readonly BlockedTarget[];
  swapPositions: readonly Position[];
  spotHoldings: readonly Holding[];
  /** 目标整体已到位：差额只是下不了单的零头，全部算到位 */
  converged: boolean;
  /** 组合下单设置里的调仓阈值：目标没收敛时按它判断未到位，和执行器同口径。不传时按后端默认值（按目标比例 5%） */
  rebalance?: Pick<OrderSettings, "rebalance_threshold_mode" | "rebalance_threshold_ratio">;
  /** 组合权益（启用成员合计），按组合权益比例的阈值要用；没读到为空 */
  equity?: number | null;
  /** 有没有策略目标；自主下单的组合没有目标，持仓都不算“不在目标内” */
  hasTarget: boolean;
}

/** 后端下单设置的默认调仓阈值：差额不超过目标数量的 5% 不调。下单设置读不出来时用它兜底。 */
const DEFAULT_THRESHOLD_RATIO = 0.05;
/** A 股一手的股数：差额不足一手视为已满足。科创板最少 200 股，但页面拿不到各标的的最小股数，统一按 100 */
const STOCK_LOT = 100;

interface Threshold {
  mode: string;
  ratio: number;
  /** 组合权益，不大于 0 表示没读到 */
  equity: number;
}

function thresholdOf(input: BuildCoinRowsInput): Threshold {
  const ratio = Number(input.rebalance?.rebalance_threshold_ratio || NaN);
  return {
    mode: input.rebalance?.rebalance_threshold_mode || "TARGET_RATIO",
    ratio: Number.isFinite(ratio) ? ratio : DEFAULT_THRESHOLD_RATIO,
    equity: input.equity ?? 0
  };
}

/**
 * 一个市场的差额执行器会不会下单，和后端 withinRebalanceThreshold 同口径：这个市场不在目标里但有持仓（按目标 0 卖出或平掉）、
 * 建仓（实际为 0）、清仓（目标为 0）、实际与目标反向时不看阈值，其余按阈值模式比较。
 */
function marketOff(
  deviation: number | null,
  target: TargetCell | undefined,
  actual: number,
  price: number,
  stock: boolean,
  threshold: Threshold
): boolean {
  if (!target) return actual !== 0;
  if (target.quantity === null || !deviation) return false;
  const want = target.quantity;
  // 整手交易（A 股）：差额不足一手视为已满足；目标为 0 时零股能一次卖完，不算
  if (stock && want !== 0 && Math.abs(deviation) < STOCK_LOT) return false;
  if (want === 0 || actual === 0 || Math.sign(want) !== Math.sign(actual)) return true;
  switch (threshold.mode) {
    case "TARGET_RATIO":
      return Math.abs(deviation) > Math.abs(want) * threshold.ratio;
    // 后端比金额：差额 × 价格 < 持仓 × 价格 × 比例，价格约掉后只比数量
    case "POSITION_RATIO":
      return Math.abs(deviation) >= Math.abs(actual) * threshold.ratio;
    // 取不到价格或组合权益时后端照样下单
    case "EQUITY_RATIO":
      return !(price > 0 && threshold.equity > 0) || Math.abs(deviation) * price >= threshold.equity * threshold.ratio;
    default:
      // ALWAYS：每期都调
      return true;
  }
}

function blankRow(coin: string): CoinRow {
  return {
    coin,
    actualSpot: 0,
    actualSwap: 0,
    actualSpotText: "0",
    actualSwapText: "0",
    memberSpot: {},
    memberSwap: {},
    deviationSpot: null,
    deviationSwap: null,
    deviationSpotText: null,
    deviationSwapText: null,
    spotBalance: 0,
    sellable: 0,
    stock: false,
    blockedReason: "",
    deferredReason: "",
    markSpot: 0,
    markSwap: 0,
    offSpot: false,
    offSwap: false,
    state: "ok",
    worstRatio: 0
  };
}

function ratioOf(deviation: number | null, target: TargetCell | undefined, actual: number): number {
  if (deviation === null || !target || target.quantity === null) return 0;
  const base = Math.abs(target.quantity);
  if (base === 0) return actual === 0 ? 0 : 1;
  return Math.abs(deviation) / base;
}

/**
 * 持仓与目标按币合成一行：目标（现货、永续）、实际（现货、永续）、偏差分市场各算各的，
 * 不合成净敞口，对冲的仓位不会互相抵消。
 */
export function buildCoinRows(input: BuildCoinRowsInput): CoinRow[] {
  const rows = new Map<string, CoinRow>();
  const rowOf = (coin: string) => {
    let row = rows.get(coin);
    if (!row) {
      row = blankRow(coin);
      rows.set(coin, row);
    }
    return row;
  };

  for (const target of input.targets) {
    const cell: TargetCell = {
      quantity: target.hold ? null : Number(target.quantity),
      text: target.hold ? "" : target.quantity,
      hold: Boolean(target.hold)
    };
    const row = rowOf(coinOf(target.instrument_id));
    const market = marketOfInstrument(target.instrument_id);
    if (market === 2) row.targetSwap = cell;
    else row.targetSpot = cell;
    if (market === 3) row.stock = true;
  }
  for (const item of input.blocked) {
    const row = rowOf(coinOf(item.instrument_id));
    if (item.deferred) row.deferredReason = row.deferredReason ? `${row.deferredReason}；${item.reason}` : item.reason;
    else row.blockedReason = row.blockedReason ? `${row.blockedReason}；${item.reason}` : item.reason;
  }
  for (const position of input.swapPositions) {
    const row = rowOf(coinOf(position.instrument_id));
    row.actualSwap += Number(position.signed_quantity);
    row.actualSwapText = decimalAdd(row.actualSwapText, position.signed_quantity);
    row.markSwap ||= Number(position.mark_price) || 0;
    addMember(row.memberSwap, position.trading_account_id, Number(position.signed_quantity));
  }
  for (const holding of input.spotHoldings) {
    const row = rowOf(holding.asset || coinOf(holding.instrument_id));
    const quantity = Number(holding.quantity);
    row.spotBalance += quantity;
    if (holding.instrument_id) {
      row.actualSpot += quantity;
      row.actualSpotText = decimalAdd(row.actualSpotText, holding.quantity);
      row.sellable += Number(holding.sellable || 0);
      row.markSpot ||= Number(holding.mark_price) || 0;
      addMember(row.memberSpot, holding.trading_account_id, quantity);
      if (marketOfInstrument(holding.instrument_id) === 3) row.stock = true;
    }
  }

  const threshold = thresholdOf(input);
  const result: CoinRow[] = [];
  for (const row of rows.values()) {
    if (row.targetSpot && row.targetSpot.quantity !== null) {
      row.deviationSpot = row.actualSpot - row.targetSpot.quantity;
      row.deviationSpotText = decimalSub(row.actualSpotText, row.targetSpot.text);
    }
    if (row.targetSwap && row.targetSwap.quantity !== null) {
      row.deviationSwap = row.actualSwap - row.targetSwap.quantity;
      row.deviationSwapText = decimalSub(row.actualSwapText, row.targetSwap.text);
    }
    const hasAnyTarget = Boolean(row.targetSpot || row.targetSwap);
    // 目标之外的持仓也要列出：其中受管的现货会被执行器按目标 0 卖掉，永续有仓位也一样。
    const listed = hasAnyTarget || row.blockedReason || row.deferredReason || row.actualSwap !== 0 || row.actualSpot !== 0;
    if (!listed) continue;
    row.worstRatio = Math.max(
      ratioOf(row.deviationSpot, row.targetSpot, row.actualSpot),
      ratioOf(row.deviationSwap, row.targetSwap, row.actualSwap)
    );
    // 没有任何目标的行不按市场判断：策略组合里标为不在目标内，自主下单的组合没有目标
    if (!input.converged && hasAnyTarget) {
      row.offSpot = marketOff(row.deviationSpot, row.targetSpot, row.actualSpot, row.markSpot, row.stock, threshold);
      row.offSwap = marketOff(row.deviationSwap, row.targetSwap, row.actualSwap, row.markSwap, false, threshold);
    }
    if (row.blockedReason) row.state = "blocked";
    else if (!hasAnyTarget && input.hasTarget) row.state = "extra";
    else if (row.offSpot || row.offSwap) row.state = "off";
    else row.state = "ok";
    result.push(row);
  }
  return result.sort((a, b) => stateOrder(a) - stateOrder(b) || b.worstRatio - a.worstRatio || a.coin.localeCompare(b.coin));
}

function stateOrder(row: CoinRow): number {
  return { blocked: 0, off: 1, extra: 2, ok: 3 }[row.state];
}

function addMember(members: Record<string, number>, accountId: string, quantity: number) {
  if (!accountId || !quantity) return;
  members[accountId] = (members[accountId] ?? 0) + quantity;
}

/**
 * 行内下单默认的执行账户：候选账户里（调用方按启用、开通了该市场筛好，保持原来的顺序）这个市场持仓绝对值最大的；
 * 一样大时取靠前的。候选账户都没有持仓时返回 undefined，由调用方按原来的选法取第一个候选。
 * 多成员组合里卖出差额要在持仓所在的账户上下，否则会在别的账户上开出反向仓。
 */
export function largestHolder(row: CoinRow, market: 1 | 2 | 3, candidates: readonly string[]): string | undefined {
  const members = market === 2 ? row.memberSwap : row.memberSpot;
  let best: string | undefined;
  let size = 0;
  for (const accountId of candidates) {
    const held = Math.abs(members[accountId] ?? 0);
    if (held > size) {
      best = accountId;
      size = held;
    }
  }
  return best;
}

export interface OrderHint {
  market: 1 | 2 | 3;
  instrumentId: string;
  /** 与目标有差额时预填的方向：差额为正要买入，为负要卖出；没有目标或保持当前持仓时为空 */
  side?: 1 | 2;
  /** 与目标的差额（绝对值） */
  quantity?: string;
}

/** 差额不为零时预填方向和数量：差额为负要买入，为正要卖出，数量是差额的绝对值（精确小数）。 */
function setDeviation(hint: OrderHint, deviation: string | null) {
  if (deviation === null || deviation === "0") return;
  hint.side = deviation.startsWith("-") ? 1 : 2;
  hint.quantity = deviation.replace(/^-/, "");
}

/**
 * 行内下单的入口：现货、永续各一个（有目标或有持仓的市场才给），带上市场和与目标的差额。
 * 币和计价币拼出内部标的 ID，两个市场都有目标时不会只能下永续。
 */
export function rowOrderHints(row: CoinRow, quote: string): OrderHint[] {
  const hints: OrderHint[] = [];
  // 股票的标的 ID 就是代码本身（600519.XSHG），只有一个市场
  if (row.stock) {
    if (!row.targetSpot && row.actualSpot === 0) return hints;
    const hint: OrderHint = { market: 3, instrumentId: row.coin };
    setDeviation(hint, row.deviationSpotText);
    return [hint];
  }
  const markets = [
    { market: 1 as const, target: row.targetSpot, actual: row.actualSpot, deviation: row.deviationSpotText, suffix: "SPOT" },
    { market: 2 as const, target: row.targetSwap, actual: row.actualSwap, deviation: row.deviationSwapText, suffix: "SWAP" }
  ];
  for (const item of markets) {
    if (!item.target && item.actual === 0) continue;
    const hint: OrderHint = { market: item.market, instrumentId: `${row.coin}-${quote}-${item.suffix}` };
    setDeviation(hint, item.deviation);
    hints.push(hint);
  }
  return hints;
}

/** 目标里涉及的币的个数：同一个币的现货、永续目标算一个。 */
export function targetCoinCount(targets: readonly InstrumentTarget[]): number {
  return new Set(targets.map(item => coinOf(item.instrument_id))).size;
}

export type PositionFilter = "all" | "off" | "holding";

export function filterCoinRows(rows: readonly CoinRow[], filter: PositionFilter, search: string): CoinRow[] {
  const keyword = search.trim().toUpperCase();
  return rows.filter(row => {
    if (keyword && !row.coin.includes(keyword)) return false;
    if (filter === "off") return row.state !== "ok";
    if (filter === "holding") return row.actualSpot !== 0 || row.actualSwap !== 0;
    return true;
  });
}

/** 显示用：浮点相减的误差只留 8 位有效数字。 */
export function cleanNumber(value: number): number {
  return Number(value.toPrecision(8));
}
