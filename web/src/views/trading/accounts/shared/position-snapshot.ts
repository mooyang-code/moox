import { listPositions } from "@/api/trade";
import type { LogicalAccount, Position } from "@/api/trade/types";
import { coinOf } from "./markets";

export interface PositionSnapshot {
  /** 永续持仓（已去掉零仓位） */
  swapPositions: Position[];
  /** 现货和股票余额，统一来自 ListPositions（market_type !== "swap"） */
  spotHoldings: Position[];
}

/**
 * 读组合账户的实际持仓：统一调用一次 ListPositions，由后端返回启用成员的永续合约持仓与现货/股票余额。
 */
export async function loadPositionSnapshot(logical: LogicalAccount): Promise<PositionSnapshot> {
  const response = await listPositions({ logical_account_id: logical.logical_account_id });
  const positions = response.positions || [];
  return {
    swapPositions: positions.filter(item => item.market_type === "swap" && Number(item.signed_quantity) !== 0),
    spotHoldings: positions.filter(item => item.market_type !== "swap")
  };
}

/** 持仓数按币算：同一个币的现货和永续算一个，和「持仓与目标」里“有持仓”的口径一致。 */
export function positionCount(snapshot: PositionSnapshot): number {
  const coins = new Set<string>();
  for (const item of snapshot.swapPositions) coins.add(coinOf(item.instrument_id));
  for (const item of snapshot.spotHoldings) {
    if (item.instrument_id && Number(item.signed_quantity) !== 0) coins.add(coinOf(item.instrument_id));
  }
  return coins.size;
}
