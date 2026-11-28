import { beforeEach, describe, expect, it, vi } from "vitest";
import type { LogicalAccount, Position } from "@/api/trade/types";
import { listPositions } from "@/api/trade";
import { loadPositionSnapshot, positionCount } from "./position-snapshot";

vi.mock("@/api/trade", () => ({
  listPositions: vi.fn()
}));

describe("读取持仓快照", () => {
  beforeEach(() => {
    vi.mocked(listPositions).mockReset();
  });

  it("A 股账户（市场 equity）统一走 ListPositions，股票持仓与可卖量进入 spotHoldings", async () => {
    vi.mocked(listPositions).mockResolvedValue({
      positions: [
        { market_type: "equity", asset: "600519.XSHG", instrument_id: "600519.XSHG", signed_quantity: "300", sellable: "100" }
      ] as Position[]
    } as never);
    const logical = { logical_account_id: "L1", markets: ["equity"], members: [] } as unknown as LogicalAccount;
    const snapshot = await loadPositionSnapshot(logical);
    expect(listPositions).toHaveBeenCalledTimes(1);
    expect(listPositions).toHaveBeenCalledWith({ logical_account_id: "L1" });
    expect(snapshot.swapPositions).toEqual([]);
    expect(snapshot.spotHoldings).toHaveLength(1);
    expect(snapshot.spotHoldings[0].sellable).toBe("100");
  });

  it("加密货币组合：单次 ListPositions 按 market_type 拆出永续（去零）与现货持仓", async () => {
    vi.mocked(listPositions).mockResolvedValue({
      positions: [
        { instrument_id: "BTC-USDT-SWAP", market_type: "swap", signed_quantity: "2" },
        { instrument_id: "ETH-USDT-SWAP", market_type: "swap", signed_quantity: "0" },
        { instrument_id: "BTC-USDT-SPOT", market_type: "spot", asset: "BTC", signed_quantity: "1" }
      ] as Position[]
    } as never);
    const logical = {
      logical_account_id: "L2",
      markets: ["spot", "swap"],
      members: []
    } as unknown as LogicalAccount;
    const snapshot = await loadPositionSnapshot(logical);
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
        { instrument_id: "BTC-USDT-SPOT", signed_quantity: "0.5" },
        { instrument_id: "XRP-USDT-SPOT", signed_quantity: "0" },
        { instrument_id: "", asset: "USDT", signed_quantity: "100" }
      ] as Position[]
    };
    expect(positionCount(snapshot)).toBe(2);
  });
});
