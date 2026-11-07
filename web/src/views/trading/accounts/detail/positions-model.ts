import type { BlockedTarget, Holding, InstrumentTarget, Position } from "@/api/trade/types";
import { coinOf, marketOfInstrument } from "../shared/markets";

export interface TargetCell {
  /** 目标数量；保持当前持仓时为 null */
  quantity: number | null;
  hold: boolean;
}

export interface CoinRow {
  coin: string;
  targetSpot?: TargetCell;
  targetSwap?: TargetCell;
  actualSpot: number;
  actualSwap: number;
  /** 实际减目标；这个市场没有目标或保持当前持仓时为 null */
  deviationSpot: number | null;
  deviationSwap: number | null;
  /** 现货钱包里这个币的总数，包含不受管理的部分 */
  spotBalance: number;
  /** 被拦下的原因，没有则为空 */
  blockedReason: string;
  state: "ok" | "off" | "blocked" | "extra";
  /** 相对偏差的最大值，用于排序 */
  worstRatio: number;
}

export interface BuildCoinRowsInput {
  targets: readonly InstrumentTarget[];
  blocked: readonly BlockedTarget[];
  swapPositions: readonly Position[];
  spotHoldings: readonly Holding[];
  /** 目标整体已收敛：差额只是下不了单的零头，全部算到位 */
  converged: boolean;
  /** 有没有策略目标；自主下单的组合没有目标，持仓都不算“目标外” */
  hasTarget: boolean;
}

/** 实际与目标相差 1% 以内算到位。 */
export const TOLERANCE_RATIO = 0.01;

function blankRow(coin: string): CoinRow {
  return {
    coin,
    actualSpot: 0,
    actualSwap: 0,
    deviationSpot: null,
    deviationSwap: null,
    spotBalance: 0,
    blockedReason: "",
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
      hold: Boolean(target.hold)
    };
    const row = rowOf(coinOf(target.instrument_id));
    if (marketOfInstrument(target.instrument_id) === 2) row.targetSwap = cell;
    else row.targetSpot = cell;
  }
  for (const item of input.blocked) {
    const row = rowOf(coinOf(item.instrument_id));
    row.blockedReason = row.blockedReason ? `${row.blockedReason}；${item.reason}` : item.reason;
  }
  for (const position of input.swapPositions) {
    rowOf(coinOf(position.instrument_id)).actualSwap += Number(position.signed_quantity);
  }
  for (const holding of input.spotHoldings) {
    const row = rowOf(holding.asset || coinOf(holding.instrument_id));
    const quantity = Number(holding.quantity);
    row.spotBalance += quantity;
    if (holding.instrument_id) row.actualSpot += quantity;
  }

  const result: CoinRow[] = [];
  for (const row of rows.values()) {
    if (row.targetSpot && row.targetSpot.quantity !== null) row.deviationSpot = row.actualSpot - row.targetSpot.quantity;
    if (row.targetSwap && row.targetSwap.quantity !== null) row.deviationSwap = row.actualSwap - row.targetSwap.quantity;
    const hasAnyTarget = Boolean(row.targetSpot || row.targetSwap);
    // 目标之外的持仓也要列出：其中受管的现货会被执行器按目标 0 卖掉，永续有仓位也一样。
    const listed = hasAnyTarget || row.blockedReason || row.actualSwap !== 0 || row.actualSpot !== 0;
    if (!listed) continue;
    row.worstRatio = Math.max(
      ratioOf(row.deviationSpot, row.targetSpot, row.actualSpot),
      ratioOf(row.deviationSwap, row.targetSwap, row.actualSwap)
    );
    if (row.blockedReason) row.state = "blocked";
    else if (!hasAnyTarget && input.hasTarget) row.state = "extra";
    else if (!input.converged && row.worstRatio > TOLERANCE_RATIO) row.state = "off";
    else row.state = "ok";
    result.push(row);
  }
  return result.sort((a, b) => stateOrder(a) - stateOrder(b) || b.worstRatio - a.worstRatio || a.coin.localeCompare(b.coin));
}

function stateOrder(row: CoinRow): number {
  return { blocked: 0, off: 1, extra: 2, ok: 3 }[row.state];
}

export interface OrderHint {
  market: 1 | 2;
  instrumentId: string;
  /** 与目标有差额时预填的方向：差额为正要买入，为负要卖出；没有目标或保持当前持仓时为空 */
  side?: 1 | 2;
  /** 与目标的差额（绝对值） */
  quantity?: string;
}

/**
 * 行内下单的入口：现货、永续各一个（有目标或有持仓的市场才给），带上市场和与目标的差额。
 * 币和计价币拼出内部标的 ID，两个市场都有目标时不会只能下永续。
 */
export function rowOrderHints(row: CoinRow, quote: string): OrderHint[] {
  const hints: OrderHint[] = [];
  const markets = [
    { market: 1 as const, target: row.targetSpot, actual: row.actualSpot, deviation: row.deviationSpot, suffix: "SPOT" },
    { market: 2 as const, target: row.targetSwap, actual: row.actualSwap, deviation: row.deviationSwap, suffix: "SWAP" }
  ];
  for (const item of markets) {
    if (!item.target && item.actual === 0) continue;
    const hint: OrderHint = { market: item.market, instrumentId: `${row.coin}-${quote}-${item.suffix}` };
    if (item.deviation !== null && Math.abs(item.deviation) > 1e-12) {
      hint.side = item.deviation < 0 ? 1 : 2;
      hint.quantity = String(cleanNumber(Math.abs(item.deviation)));
    }
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
