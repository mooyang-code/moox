import { describe, expect, it } from "vitest";
import type { Holding, Position } from "@/api/trade/types";
import { buildCoinRows, filterCoinRows } from "./positions-model";

const swap = (instrument_id: string, signed_quantity: string) => ({ instrument_id, signed_quantity, market_type: 2 }) as Position;
const spot = (asset: string, quantity: string, instrument_id = `${asset}-USDT-SPOT`) =>
  ({ asset, quantity, instrument_id }) as Holding;

describe("持仓与目标按币合并", () => {
  it("同一个币的现货和永续合成一行，偏差分市场各算各的", () => {
    const rows = buildCoinRows({
      targets: [
        { instrument_id: "BTC-USDT-SPOT", quantity: "1" },
        { instrument_id: "BTC-USDT-SWAP", quantity: "-1" }
      ],
      blocked: [],
      swapPositions: [swap("BTC-USDT-SWAP", "-0.4")],
      spotHoldings: [spot("BTC", "1.5")],
      converged: false,
      hasTarget: true
    });
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ coin: "BTC", actualSpot: 1.5, actualSwap: -0.4, spotBalance: 1.5, state: "off" });
    // 对冲仓位不能被净额抵消：现货偏差 +0.5，永续偏差 +0.6
    expect(rows[0].deviationSpot).toBeCloseTo(0.5);
    expect(rows[0].deviationSwap).toBeCloseTo(0.6);
  });

  it("相差 1% 以内或目标已收敛算到位", () => {
    const input = {
      targets: [{ instrument_id: "ETH-USDT-SWAP", quantity: "10" }],
      blocked: [],
      swapPositions: [swap("ETH-USDT-SWAP", "9.95")],
      spotHoldings: [],
      hasTarget: true
    };
    expect(buildCoinRows({ ...input, converged: false })[0].state).toBe("ok");
    const far = { ...input, swapPositions: [swap("ETH-USDT-SWAP", "5")] };
    expect(buildCoinRows({ ...far, converged: false })[0].state).toBe("off");
    expect(buildCoinRows({ ...far, converged: true })[0].state).toBe("ok");
  });

  it("被拦下的目标优先显示原因，目标外的永续持仓标为目标外", () => {
    const rows = buildCoinRows({
      targets: [{ instrument_id: "DOGE-USDT-SPOT", quantity: "100" }],
      blocked: [{ instrument_id: "DOGE-USDT-SPOT", quantity: "100", reason: "低于最小下单额" }],
      swapPositions: [swap("SOL-USDT-SWAP", "2")],
      spotHoldings: [spot("XRP", "50")],
      converged: false,
      hasTarget: true
    });
    expect(rows.map(row => [row.coin, row.state])).toEqual([
      ["DOGE", "blocked"],
      ["SOL", "extra"]
    ]);
    expect(rows[0].blockedReason).toContain("最小下单额");
  });

  it("保持当前持仓的目标没有偏差；自主下单的组合列出现货持有且不算目标外", () => {
    const hold = buildCoinRows({
      targets: [{ instrument_id: "BTC-USDT-SWAP", quantity: "", hold: true }],
      blocked: [],
      swapPositions: [swap("BTC-USDT-SWAP", "3")],
      spotHoldings: [],
      converged: false,
      hasTarget: true
    });
    expect(hold[0]).toMatchObject({ deviationSwap: null, state: "ok", actualSwap: 3 });
    const manual = buildCoinRows({
      targets: [],
      blocked: [],
      swapPositions: [swap("SOL-USDT-SWAP", "2")],
      spotHoldings: [spot("BTC", "0.2"), spot("USDT", "100", "")],
      converged: false,
      hasTarget: false
    });
    expect(manual.map(row => [row.coin, row.state])).toEqual([
      ["BTC", "ok"],
      ["SOL", "ok"]
    ]);
  });

  it("筛选未到位、有持仓并按币名搜索", () => {
    const rows = buildCoinRows({
      targets: [
        { instrument_id: "BTC-USDT-SWAP", quantity: "1" },
        { instrument_id: "ETH-USDT-SWAP", quantity: "0" }
      ],
      blocked: [],
      swapPositions: [swap("BTC-USDT-SWAP", "1")],
      spotHoldings: [],
      converged: false,
      hasTarget: true
    });
    expect(filterCoinRows(rows, "off", "")).toEqual([]);
    expect(filterCoinRows(rows, "holding", "").map(row => row.coin)).toEqual(["BTC"]);
    expect(filterCoinRows(rows, "all", "et").map(row => row.coin)).toEqual(["ETH"]);
  });
});
