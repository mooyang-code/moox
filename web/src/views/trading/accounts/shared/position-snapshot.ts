import { listHoldings, listPositions } from "@/api/trade";
import type { Holding, LogicalAccount, Position, TradingAccount } from "@/api/trade/types";

export interface PositionSnapshot {
  /** 永续持仓（已去掉零仓位） */
  swapPositions: Position[];
  /** 现货余额，来自开通了现货的启用成员 */
  spotHoldings: Holding[];
}

/**
 * 读组合账户的实际持仓：永续来自 ListPositions，现货来自成员的 ListHoldings。
 * ListHoldings 对没开通现货的账户会报错，所以只查开通了现货的启用成员；两处数据各管各的市场，不会重复计数。
 */
export async function loadPositionSnapshot(
  logical: LogicalAccount,
  accounts: ReadonlyMap<string, TradingAccount>
): Promise<PositionSnapshot> {
  const spotMembers = (logical.members || []).filter(
    member => member.enabled && accounts.get(member.trading_account_id)?.markets?.includes(1)
  );
  const [positions, holdings] = await Promise.all([
    logical.markets?.includes(2)
      ? listPositions({ logical_account_id: logical.logical_account_id })
      : Promise.resolve({ positions: [] as Position[] }),
    Promise.all(spotMembers.map(member => listHoldings(member.trading_account_id)))
  ]);
  return {
    swapPositions: (positions.positions || []).filter(item => item.market_type === 2 && Number(item.signed_quantity) !== 0),
    spotHoldings: holdings.flatMap(item => item.holdings || [])
  };
}

/** 持仓数：永续的非零仓位数，加上现货里有标的的非零持有数。 */
export function positionCount(snapshot: PositionSnapshot): number {
  return (
    snapshot.swapPositions.length + snapshot.spotHoldings.filter(item => item.instrument_id && Number(item.quantity) !== 0).length
  );
}
