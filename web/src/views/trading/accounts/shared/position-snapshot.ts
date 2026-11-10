import { listHoldings, listPositions } from "@/api/trade";
import type { Holding, LogicalAccount, Position, TradingAccount } from "@/api/trade/types";
import { coinOf } from "./markets";

export interface PositionSnapshot {
  /** 永续持仓（已去掉零仓位） */
  swapPositions: Position[];
  /** 现货和股票余额，来自开通了现货或股票的启用成员 */
  spotHoldings: Holding[];
}

/**
 * 读组合账户的实际持仓：永续来自 ListPositions，现货和股票来自成员的 ListHoldings（持仓来自余额的市场）。
 * ListHoldings 对只开通永续的账户会报错，所以只查开通了现货或股票的启用成员；两处数据各管各的市场，不会重复计数。
 */
export async function loadPositionSnapshot(
  logical: LogicalAccount,
  accounts: ReadonlyMap<string, TradingAccount>,
  options: { silent?: boolean } = {}
): Promise<PositionSnapshot> {
  const balanceMembers = (logical.members || []).filter(
    member => member.enabled && accounts.get(member.trading_account_id)?.markets?.some(market => market === 1 || market === 3)
  );
  const [positions, holdings] = await Promise.all([
    logical.markets?.includes(2)
      ? listPositions({ logical_account_id: logical.logical_account_id }, options)
      : Promise.resolve({ positions: [] as Position[] }),
    Promise.all(balanceMembers.map(member => listHoldings(member.trading_account_id, options)))
  ]);
  return {
    swapPositions: (positions.positions || []).filter(item => item.market_type === 2 && Number(item.signed_quantity) !== 0),
    spotHoldings: holdings.flatMap(item => item.holdings || [])
  };
}

/** 持仓数按币算：同一个币的现货和永续算一个，和「持仓与目标」里“有持仓”的口径一致。 */
export function positionCount(snapshot: PositionSnapshot): number {
  const coins = new Set<string>();
  for (const item of snapshot.swapPositions) coins.add(coinOf(item.instrument_id));
  for (const item of snapshot.spotHoldings) {
    if (item.instrument_id && Number(item.quantity) !== 0) coins.add(coinOf(item.instrument_id));
  }
  return coins.size;
}
