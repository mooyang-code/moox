import { describe, expect, it } from "vitest";
import type { Holding, Position } from "@/api/trade/types";
import { decimalAdd, decimalSub } from "../shared/order-rules";
import { buildCoinRows, filterCoinRows, rowOrderHints, targetCoinCount } from "./positions-model";

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

  it("相差 1% 以内或目标整体已到位算到位", () => {
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

  it("未执行的目标优先显示原因，不在目标内的永续持仓标为 extra", () => {
    const rows = buildCoinRows({
      targets: [{ instrument_id: "DOGE-USDT-SPOT", quantity: "100" }],
      blocked: [{ instrument_id: "DOGE-USDT-SPOT", quantity: "100", reason: "低于最小下单额" }],
      swapPositions: [swap("SOL-USDT-SWAP", "2")],
      spotHoldings: [spot("XRP", "50")],
      converged: false,
      hasTarget: true
    });
    // 目标之外的现货持仓（可能被执行器按目标 0 卖掉）也要列出
    expect(rows.map(row => [row.coin, row.state])).toEqual([
      ["DOGE", "blocked"],
      ["SOL", "extra"],
      ["XRP", "extra"]
    ]);
    expect(rows[0].blockedReason).toContain("最小下单额");
  });

  it("被每小时上限顺延的目标不算未执行：状态按差额判断，原因单独放在顺延里", () => {
    const rows = buildCoinRows({
      targets: [{ instrument_id: "DOGE-USDT-SPOT", quantity: "100" }],
      blocked: [{ instrument_id: "DOGE-USDT-SPOT", quantity: "100", reason: "超出每小时上限，留到下一个整点", deferred: true }],
      swapPositions: [],
      spotHoldings: [spot("DOGE", "10")],
      converged: false,
      hasTarget: true
    });
    expect(rows[0].state).toBe("off");
    expect(rows[0].blockedReason).toBe("");
    expect(rows[0].deferredReason).toContain("下一个整点");
  });

  it("保持当前持仓的目标没有偏差；自主下单的组合列出现货持有且不算不在目标内", () => {
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

  it("行内下单：现货、永续各一个入口，带市场、方向和与目标的差额", () => {
    const [row] = buildCoinRows({
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
    expect(rowOrderHints(row, "USDT")).toEqual([
      // 现货多了 0.5：卖出 0.5
      { market: 1, instrumentId: "BTC-USDT-SPOT", side: 2, quantity: "0.5" },
      // 永续目标 -1 实际 -0.4：还差 -0.6，卖出 0.6
      { market: 2, instrumentId: "BTC-USDT-SWAP", side: 2, quantity: "0.6" }
    ]);
  });

  it("预填数量用小数精确相减，不经过浮点和有效位数截断", () => {
    const [row] = buildCoinRows({
      targets: [
        { instrument_id: "BTC-USDT-SPOT", quantity: "100000.1" },
        { instrument_id: "BTC-USDT-SWAP", quantity: "-0.00000001" }
      ],
      blocked: [],
      swapPositions: [swap("BTC-USDT-SWAP", "123456.78912345"), swap("BTC-USDT-SWAP", "-0.1")],
      spotHoldings: [spot("BTC", "123456.78912345")],
      converged: false,
      hasTarget: true
    });
    expect(rowOrderHints(row, "USDT")).toEqual([
      { market: 1, instrumentId: "BTC-USDT-SPOT", side: 2, quantity: "23456.68912345" },
      { market: 2, instrumentId: "BTC-USDT-SWAP", side: 2, quantity: "123456.68912346" }
    ]);
  });

  it("小数相加减：带符号、结果为负或为零", () => {
    expect(decimalSub("0.3", "0.1")).toBe("0.2");
    expect(decimalSub("1", "1.5")).toBe("-0.5");
    expect(decimalSub("-1.25", "-1.25")).toBe("0");
    expect(decimalAdd("-0.4", "0.1")).toBe("-0.3");
    expect(decimalAdd("123456.78912345", "0.00000001")).toBe("123456.78912346");
  });

  it("没有目标或保持当前持仓时只给市场，不预填方向和数量；没有目标也没有持仓的市场不给入口", () => {
    const [hold] = buildCoinRows({
      targets: [{ instrument_id: "ETH-USDT-SWAP", quantity: "", hold: true }],
      blocked: [],
      swapPositions: [swap("ETH-USDT-SWAP", "3")],
      spotHoldings: [],
      converged: false,
      hasTarget: true
    });
    expect(rowOrderHints(hold, "USDT")).toEqual([{ market: 2, instrumentId: "ETH-USDT-SWAP" }]);
    const [buy] = buildCoinRows({
      targets: [{ instrument_id: "SOL-USDT-SWAP", quantity: "10" }],
      blocked: [],
      swapPositions: [],
      spotHoldings: [],
      converged: false,
      hasTarget: true
    });
    expect(rowOrderHints(buy, "USDT")).toEqual([{ market: 2, instrumentId: "SOL-USDT-SWAP", side: 1, quantity: "10" }]);
  });

  it("目标个数按币算，同一个币的现货、永续目标算一个", () => {
    expect(
      targetCoinCount([
        { instrument_id: "BTC-USDT-SPOT", quantity: "1" },
        { instrument_id: "BTC-USDT-SWAP", quantity: "1" },
        { instrument_id: "ETH-USDT-SWAP", quantity: "1" }
      ])
    ).toBe(2);
  });
});

describe("A 股持仓", () => {
  it("股票一行带可卖数量，T+1 顺延显示原因，行内下单用股票代码", () => {
    const rows = buildCoinRows({
      targets: [{ instrument_id: "600519.XSHG", quantity: "0" }],
      blocked: [{ instrument_id: "600519.XSHG", quantity: "-300", reason: "当日买入，次日可卖", deferred: true }],
      swapPositions: [],
      spotHoldings: [{ asset: "600519.XSHG", quantity: "300", sellable: "100", instrument_id: "600519.XSHG" } as Holding],
      converged: true,
      hasTarget: true
    });
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ coin: "600519.XSHG", stock: true, actualSpot: 300, sellable: 100, state: "ok" });
    expect(rows[0].deferredReason).toBe("当日买入，次日可卖");
    expect(rowOrderHints(rows[0], "CNY")).toEqual([{ market: 3, instrumentId: "600519.XSHG", side: 2, quantity: "300" }]);
  });
});
