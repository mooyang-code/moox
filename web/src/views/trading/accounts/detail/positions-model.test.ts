import { describe, expect, it } from "vitest";
import type { Holding, Position } from "@/api/trade/types";
import { decimalAdd, decimalSub } from "../shared/order-rules";
import { buildCoinRows, filterCoinRows, rowOrderHints, targetCoinCount } from "./positions-model";

const swap = (instrument_id: string, signed_quantity: string, mark_price = "") =>
  ({ instrument_id, signed_quantity, mark_price, market_type: 2 }) as Position;
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

  it("未收敛时按下单设置的调仓阈值判断：阈值内算到位，阈值外算未到位；目标整体已到位全部算到位", () => {
    const input = {
      targets: [{ instrument_id: "ETH-USDT-SWAP", quantity: "10" }],
      blocked: [],
      spotHoldings: [],
      hasTarget: true,
      rebalance: { rebalance_threshold_mode: "TARGET_RATIO", rebalance_threshold_ratio: "0.05" }
    };
    const stateAt = (actual: string, converged = false) =>
      buildCoinRows({ ...input, swapPositions: [swap("ETH-USDT-SWAP", actual)], converged })[0].state;
    expect(stateAt("9.6")).toBe("ok");
    expect(stateAt("9.5")).toBe("ok");
    expect(stateAt("9.4")).toBe("off");
    expect(stateAt("5")).toBe("off");
    expect(stateAt("5", true)).toBe("ok");
    // 阈值改成 10%：9.4 也在阈值内
    const wide = { ...input.rebalance, rebalance_threshold_ratio: "0.1" };
    expect(
      buildCoinRows({ ...input, rebalance: wide, swapPositions: [swap("ETH-USDT-SWAP", "9.4")], converged: false })[0].state
    ).toBe("ok");
  });

  it("拿不到下单设置时按后端默认的目标比例 5% 判断", () => {
    const input = {
      targets: [{ instrument_id: "ETH-USDT-SWAP", quantity: "10" }],
      blocked: [],
      spotHoldings: [],
      hasTarget: true
    };
    expect(buildCoinRows({ ...input, swapPositions: [swap("ETH-USDT-SWAP", "9.6")], converged: false })[0].state).toBe("ok");
    expect(buildCoinRows({ ...input, swapPositions: [swap("ETH-USDT-SWAP", "9.4")], converged: false })[0].state).toBe("off");
  });

  it("建仓、清仓、与目标反向时不看阈值，有差额就算未到位", () => {
    const rebalance = { rebalance_threshold_mode: "TARGET_RATIO", rebalance_threshold_ratio: "1" };
    const stateOf = (target: string, actual: string) =>
      buildCoinRows({
        targets: [{ instrument_id: "ETH-USDT-SWAP", quantity: target }],
        blocked: [],
        swapPositions: actual === "0" ? [] : [swap("ETH-USDT-SWAP", actual)],
        spotHoldings: [],
        converged: false,
        hasTarget: true,
        rebalance
      })[0].state;
    expect(stateOf("10", "0")).toBe("off");
    expect(stateOf("0", "2")).toBe("off");
    expect(stateOf("10", "-2")).toBe("off");
    expect(stateOf("10", "2")).toBe("ok");
  });

  it("按持仓比例的阈值只比数量；每期都调时有差额就算未到位", () => {
    const stateOf = (mode: string, actual: string) =>
      buildCoinRows({
        targets: [{ instrument_id: "ETH-USDT-SWAP", quantity: "10" }],
        blocked: [],
        swapPositions: [swap("ETH-USDT-SWAP", actual)],
        spotHoldings: [],
        converged: false,
        hasTarget: true,
        rebalance: { rebalance_threshold_mode: mode, rebalance_threshold_ratio: "0.1" }
      })[0].state;
    // 差额 1，持仓 11 × 0.1 = 1.1：在阈值内
    expect(stateOf("POSITION_RATIO", "11")).toBe("ok");
    // 差额 1.5，持仓 8.5 × 0.1 = 0.85：超出阈值
    expect(stateOf("POSITION_RATIO", "8.5")).toBe("off");
    expect(stateOf("ALWAYS", "9.99")).toBe("off");
    expect(stateOf("ALWAYS", "10")).toBe("ok");
  });

  it("按组合权益比例的阈值：差额金额低于权益 × 比例算到位；缺价格或缺权益时执行器照调，算未到位", () => {
    const stateOf = (actual: string, markPrice: string, equity: number | null) =>
      buildCoinRows({
        targets: [{ instrument_id: "ETH-USDT-SWAP", quantity: "10" }],
        blocked: [],
        swapPositions: [swap("ETH-USDT-SWAP", actual, markPrice)],
        spotHoldings: [],
        converged: false,
        hasTarget: true,
        equity,
        rebalance: { rebalance_threshold_mode: "EQUITY_RATIO", rebalance_threshold_ratio: "0.05" }
      })[0].state;
    // 权益 10000 × 0.05 = 500：差额 1 × 400 = 400 在阈值内，差额 2 × 400 = 800 超出
    expect(stateOf("9", "400", 10000)).toBe("ok");
    expect(stateOf("8", "400", 10000)).toBe("off");
    expect(stateOf("5", "400", 10000)).toBe("off");
    expect(stateOf("9", "", 10000)).toBe("off");
    expect(stateOf("9", "400", null)).toBe("off");
  });

  it("有目标的币在另一个市场没有目标但有持仓：执行器按目标 0 平掉，算未到位，只给那个市场着色", () => {
    const build = (converged: boolean) =>
      buildCoinRows({
        targets: [{ instrument_id: "BTC-USDT-SPOT", quantity: "1" }],
        blocked: [],
        swapPositions: [swap("BTC-USDT-SWAP", "2")],
        spotHoldings: [spot("BTC", "0.98")],
        converged,
        hasTarget: true
      })[0];
    expect(build(false)).toMatchObject({ state: "off", offSpot: false, offSwap: true });
    expect(build(true)).toMatchObject({ state: "ok", offSpot: false, offSwap: false });
  });

  it("按市场记录未到位：永续超出阈值时，阈值内的现货不算", () => {
    const [row] = buildCoinRows({
      targets: [
        { instrument_id: "BTC-USDT-SPOT", quantity: "1" },
        { instrument_id: "BTC-USDT-SWAP", quantity: "-1" }
      ],
      blocked: [],
      swapPositions: [swap("BTC-USDT-SWAP", "-0.5")],
      spotHoldings: [spot("BTC", "0.98")],
      converged: false,
      hasTarget: true
    });
    expect(row).toMatchObject({ state: "off", offSpot: false, offSwap: true });
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

  it("未收敛时差额不足一手算到位；目标为 0 时零股会卖掉，算未到位", () => {
    const stateOf = (target: string, actual: string) =>
      buildCoinRows({
        targets: [{ instrument_id: "600519.XSHG", quantity: target }],
        blocked: [],
        swapPositions: [],
        spotHoldings:
          actual === "0"
            ? []
            : [{ asset: "600519.XSHG", quantity: actual, sellable: actual, instrument_id: "600519.XSHG" } as Holding],
        converged: false,
        hasTarget: true,
        rebalance: { rebalance_threshold_mode: "TARGET_RATIO", rebalance_threshold_ratio: "0.05" }
      })[0].state;
    // 差额 50 股，超出 5% 但不足一手
    expect(stateOf("350", "300")).toBe("ok");
    // 建仓不足一手也视为已满足
    expect(stateOf("80", "0")).toBe("ok");
    // 差额整整一手且超出阈值
    expect(stateOf("400", "300")).toBe("off");
    expect(stateOf("0", "50")).toBe("off");
  });
});
