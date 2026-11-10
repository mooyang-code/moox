import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Holding, LogicalAccount, Position, TradingAccount } from "@/api/trade/types";
import { listHoldings, listPositions } from "@/api/trade";
import { loadPositionSnapshot, positionCount } from "./position-snapshot";

vi.mock("@/api/trade", () => ({
  listHoldings: vi.fn(),
  listPositions: vi.fn()
}));

function member(id: string, enabled = true) {
  return { trading_account_id: id, enabled };
}

function account(id: string, markets: number[]): [string, TradingAccount] {
  return [id, { trading_account_id: id, markets } as TradingAccount];
}

describe("读取持仓快照", () => {
  beforeEach(() => {
    vi.mocked(listHoldings).mockReset();
    vi.mocked(listPositions).mockReset();
  });

  it("A 股账户（市场 3）查 ListHoldings，股票持仓与可卖量进入快照，不查永续", async () => {
    vi.mocked(listHoldings).mockResolvedValue({
      holdings: [{ asset: "600519.XSHG", instrument_id: "600519.XSHG", quantity: "300", sellable: "100" } as Holding]
    } as never);
    const logical = { logical_account_id: "L1", markets: [3], members: [member("A1")] } as unknown as LogicalAccount;
    const snapshot = await loadPositionSnapshot(logical, new Map([account("A1", [3])]));
    expect(listHoldings).toHaveBeenCalledTimes(1);
    expect(vi.mocked(listHoldings).mock.calls[0][0]).toBe("A1");
    expect(listPositions).not.toHaveBeenCalled();
    expect(snapshot.swapPositions).toEqual([]);
    expect(snapshot.spotHoldings).toHaveLength(1);
    expect(snapshot.spotHoldings[0].sellable).toBe("100");
  });

  it("加密货币组合：现货成员查 ListHoldings，永续查 ListPositions，只开通永续或已停用的成员不查余额", async () => {
    vi.mocked(listHoldings).mockResolvedValue({
      holdings: [{ asset: "BTC", instrument_id: "BTC-USDT-SPOT", quantity: "1" } as Holding]
    } as never);
    vi.mocked(listPositions).mockResolvedValue({
      positions: [
        { instrument_id: "BTC-USDT-SWAP", market_type: 2, signed_quantity: "2" },
        { instrument_id: "ETH-USDT-SWAP", market_type: 2, signed_quantity: "0" }
      ] as Position[]
    } as never);
    const logical = {
      logical_account_id: "L2",
      markets: [1, 2],
      members: [member("S1"), member("W1"), member("S2", false)]
    } as unknown as LogicalAccount;
    const accounts = new Map([account("S1", [1]), account("W1", [2]), account("S2", [1])]);
    const snapshot = await loadPositionSnapshot(logical, accounts);
    expect(vi.mocked(listHoldings).mock.calls.map(call => call[0])).toEqual(["S1"]);
    expect(listPositions).toHaveBeenCalledTimes(1);
    expect(snapshot.swapPositions.map(item => item.instrument_id)).toEqual(["BTC-USDT-SWAP"]);
    expect(snapshot.spotHoldings.map(item => item.instrument_id)).toEqual(["BTC-USDT-SPOT"]);
  });
});

describe("持仓数", () => {
  it("按币算：同一个币的现货和永续算一个，零持有和没有标的的余额不算", () => {
    const snapshot = {
      swapPositions: [
        { instrument_id: "BTC-USDT-SWAP", signed_quantity: "1" },
        { instrument_id: "ETH-USDT-SWAP", signed_quantity: "-2" }
      ] as Position[],
      spotHoldings: [
        { instrument_id: "BTC-USDT-SPOT", quantity: "0.5" },
        { instrument_id: "XRP-USDT-SPOT", quantity: "0" },
        { instrument_id: "", asset: "USDT", quantity: "100" }
      ] as Holding[]
    };
    expect(positionCount(snapshot)).toBe(2);
  });
});
