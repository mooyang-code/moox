import { describe, expect, it } from "vitest";
import { actualFromHoldings, buildComparison } from "./target-comparison";
import type { Holding } from "@/api/trade";

describe("目标与实际持仓对照", () => {
  it("计算差额并标出状态", () => {
    const rows = buildComparison(
      [
        { instrument_id: "BTC-USDT-SWAP", quantity: "1" },
        { instrument_id: "ETH-USDT-SWAP", quantity: "2" },
        { instrument_id: "SOL-USDT-SWAP", quantity: "0", hold: true }
      ],
      [{ instrument_id: "ETH-USDT-SWAP", quantity: "2", reason: "余额不足" }],
      { "BTC-USDT-SWAP": 0.4, "SOL-USDT-SWAP": 3 }
    );
    expect(rows[0]).toMatchObject({ difference: "0.6", status: "收敛中" });
    expect(rows[1].status).toBe("已阻塞：余额不足");
    expect(rows[2]).toMatchObject({ target: "保持", status: "保持当前持仓" });
  });

  it("列出目标之外仍持有的标的", () => {
    const rows = buildComparison([], [], { "BTC-USDT-SWAP": 1 });
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ target: "0", difference: "-1" });
  });

  it("没有标的的资产不参与对照", () => {
    const holdings = [
      { instrument_id: "", quantity: "9", asset: "USDT" },
      { instrument_id: "BTC-USDT-SPOT", quantity: "1", asset: "BTC" }
    ] as Holding[];
    expect(actualFromHoldings(holdings)).toEqual({ "BTC-USDT-SPOT": 1 });
  });
});
