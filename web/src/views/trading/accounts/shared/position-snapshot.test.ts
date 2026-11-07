import { describe, expect, it } from "vitest";
import type { Holding, Position } from "@/api/trade/types";
import { positionCount } from "./position-snapshot";

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
