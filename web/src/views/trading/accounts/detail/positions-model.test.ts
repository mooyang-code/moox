import { describe, expect, it } from "vitest";
import type { Holding, Position, TargetLane, TargetLaneState } from "@/api/trade/types";
import { decimalAdd, decimalSub } from "../shared/order-rules";
import {
  buildCoinRows,
  filterCoinRows,
  hasSpotSide,
  hasSwapSide,
  largestHolder,
  rowOrderHints,
  targetCoinCount
} from "./positions-model";

const swap = (instrument_id: string, signed_quantity: string, mark_price = "") =>
  ({ instrument_id, signed_quantity, mark_price, market_type: 2 }) as Position;
const spot = (asset: string, quantity: string, instrument_id = `${asset}-USDT-SPOT`) =>
  ({ asset, quantity, instrument_id }) as Holding;
/** 后端给的一条结论：0 未知、1 还会下单、2 已到位、3 低于最小下单额、4 市场规则顺延、5 不足一手、6 没有成员能下单 */
const lane = (instrument_id: string, state: TargetLaneState, reason = ""): TargetLane => ({ instrument_id, state, reason });

describe("持仓与目标按币合并", () => {
  it("同一个币的现货和永续合成一行，偏差分市场各算各的", () => {
    const rows = buildCoinRows({
      targets: [
        { instrument_id: "BTC-USDT-SPOT", quantity: "1" },
        { instrument_id: "BTC-USDT-SWAP", quantity: "-1" }
      ],
      blocked: [],
      lanes: [lane("BTC-USDT-SPOT", 1), lane("BTC-USDT-SWAP", 1)],
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

  it("是否到位只看后端给的结论，页面不按差额自己算；目标整体已到位全部算到位", () => {
    const input = {
      targets: [{ instrument_id: "ETH-USDT-SWAP", quantity: "10" }],
      blocked: [],
      spotHoldings: [],
      hasTarget: true
    };
    const rowAt = (actual: string, state: TargetLaneState, converged = false) =>
      buildCoinRows({
        ...input,
        lanes: [lane("ETH-USDT-SWAP", state)],
        swapPositions: [swap("ETH-USDT-SWAP", actual)],
        converged
      })[0];
    // 同样的差额，后端说到位就到位、说还会下单就未到位
    expect(rowAt("5", 2)).toMatchObject({ state: "ok", offSwap: false });
    expect(rowAt("5", 1)).toMatchObject({ state: "off", offSwap: true });
    expect(rowAt("9.99", 1).state).toBe("off");
    // 没有成员能下单：有差额没处理，算未到位，带上原因
    expect(rowAt("5", 6).state).toBe("off");
    expect(
      buildCoinRows({
        ...input,
        lanes: [lane("ETH-USDT-SWAP", 6, "没有启用的成员支持该标的")],
        swapPositions: [],
        converged: false
      })[0].settledReason
    ).toBe("没有启用的成员支持该标的");
    // 目标整体已收敛：后端不给结论，整页到位
    expect(buildCoinRows({ ...input, lanes: [], swapPositions: [swap("ETH-USDT-SWAP", "5")], converged: true })[0].state).toBe(
      "ok"
    );
  });

  it("有差额但执行器不会下单：算到位，并写明原因", () => {
    const rowOf = (state: TargetLaneState, reason = "") =>
      buildCoinRows({
        targets: [{ instrument_id: "ETH-USDT-SWAP", quantity: "10" }],
        blocked: [],
        lanes: [lane("ETH-USDT-SWAP", state, reason)],
        swapPositions: [swap("ETH-USDT-SWAP", "9.99")],
        spotHoldings: [],
        converged: false,
        hasTarget: true
      })[0];
    expect(rowOf(3)).toMatchObject({ state: "ok", settledReason: "差额低于最小下单额" });
    expect(rowOf(5)).toMatchObject({ state: "ok", settledReason: "差额不足一手" });
    expect(rowOf(4, "停牌")).toMatchObject({ state: "ok", settledReason: "停牌" });
    expect(rowOf(2)).toMatchObject({ state: "ok", settledReason: "" });
  });

  it("后端判断不了（报价不在缓存里）或没给这个标的的结论：显示未知，不当成到位也不当成未到位", () => {
    const input = {
      targets: [{ instrument_id: "ETH-USDT-SWAP", quantity: "10" }],
      blocked: [],
      swapPositions: [swap("ETH-USDT-SWAP", "5")],
      spotHoldings: [],
      converged: false,
      hasTarget: true
    };
    // state 缺失和 0 一样按未知处理
    expect(buildCoinRows({ ...input, lanes: [{ instrument_id: "ETH-USDT-SWAP" }] })[0]).toMatchObject({
      state: "unknown",
      offSwap: false
    });
    expect(buildCoinRows({ ...input, lanes: [lane("ETH-USDT-SWAP", 0)] })[0].state).toBe("unknown");
    expect(buildCoinRows({ ...input, lanes: [] })[0].state).toBe("unknown");
    // 一个市场还会下单、另一个未知：整行算未到位
    const mixed = buildCoinRows({
      ...input,
      targets: [...input.targets, { instrument_id: "ETH-USDT-SPOT", quantity: "1" }],
      lanes: [lane("ETH-USDT-SWAP", 1)]
    })[0];
    expect(mixed).toMatchObject({ state: "off", offSwap: true, offSpot: false });
  });

  it("两个成员一多一空合计为 0：后端说还会下单就算未到位，不因为合计持仓为 0 当成到位", () => {
    const [row] = buildCoinRows({
      targets: [{ instrument_id: "BTC-USDT-SPOT", quantity: "1" }],
      blocked: [],
      lanes: [lane("BTC-USDT-SPOT", 2), lane("BTC-USDT-SWAP", 1)],
      swapPositions: [swap("BTC-USDT-SWAP", "2"), swap("BTC-USDT-SWAP", "-2")],
      spotHoldings: [spot("BTC", "1")],
      converged: false,
      hasTarget: true
    });
    expect(row).toMatchObject({ actualSwap: 0, state: "off", offSwap: true, offSpot: false });
  });

  it("没有目标的市场后端没给结论：执行器不管这部分持仓，不算未到位也不算未知", () => {
    const [row] = buildCoinRows({
      targets: [{ instrument_id: "BTC-USDT-SWAP", quantity: "-1" }],
      blocked: [],
      lanes: [lane("BTC-USDT-SWAP", 2)],
      swapPositions: [swap("BTC-USDT-SWAP", "-1")],
      spotHoldings: [spot("BTC", "1")],
      converged: false,
      hasTarget: true
    });
    expect(row).toMatchObject({ state: "ok", offSpot: false, offSwap: false });
  });

  it("有目标的币在另一个市场没有目标但有持仓：执行器按目标 0 平掉，算未到位，只给那个市场着色", () => {
    const build = (converged: boolean) =>
      buildCoinRows({
        targets: [{ instrument_id: "BTC-USDT-SPOT", quantity: "1" }],
        blocked: [],
        lanes: converged ? [] : [lane("BTC-USDT-SPOT", 2), lane("BTC-USDT-SWAP", 1)],
        swapPositions: [swap("BTC-USDT-SWAP", "2")],
        spotHoldings: [spot("BTC", "0.98")],
        converged,
        hasTarget: true
      })[0];
    expect(build(false)).toMatchObject({ state: "off", offSpot: false, offSwap: true });
    expect(build(true)).toMatchObject({ state: "ok", offSpot: false, offSwap: false });
  });

  it("按市场记录未到位：永续还会下单时，已到位的现货不算", () => {
    const [row] = buildCoinRows({
      targets: [
        { instrument_id: "BTC-USDT-SPOT", quantity: "1" },
        { instrument_id: "BTC-USDT-SWAP", quantity: "-1" }
      ],
      blocked: [],
      lanes: [lane("BTC-USDT-SPOT", 2), lane("BTC-USDT-SWAP", 1)],
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
      lanes: [],
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

  it("被每小时上限顺延的目标不算未执行：状态按后端的结论，原因单独放在顺延里", () => {
    const rows = buildCoinRows({
      targets: [{ instrument_id: "DOGE-USDT-SPOT", quantity: "100" }],
      blocked: [{ instrument_id: "DOGE-USDT-SPOT", quantity: "100", reason: "超出每小时上限，留到下一个整点", deferred: true }],
      lanes: [lane("DOGE-USDT-SPOT", 1)],
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
      lanes: [lane("BTC-USDT-SWAP", 2)],
      swapPositions: [swap("BTC-USDT-SWAP", "3")],
      spotHoldings: [],
      converged: false,
      hasTarget: true
    });
    expect(hold[0]).toMatchObject({ deviationSwap: null, state: "ok", actualSwap: 3 });
    const manual = buildCoinRows({
      targets: [],
      blocked: [],
      lanes: [],
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
      lanes: [lane("BTC-USDT-SWAP", 2), lane("ETH-USDT-SWAP", 2)],
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
      lanes: [],
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
      lanes: [],
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
      lanes: [],
      swapPositions: [swap("ETH-USDT-SWAP", "3")],
      spotHoldings: [],
      converged: false,
      hasTarget: true
    });
    expect(rowOrderHints(hold, "USDT")).toEqual([{ market: 2, instrumentId: "ETH-USDT-SWAP" }]);
    const [buy] = buildCoinRows({
      targets: [{ instrument_id: "SOL-USDT-SWAP", quantity: "10" }],
      blocked: [],
      lanes: [],
      swapPositions: [],
      spotHoldings: [],
      converged: false,
      hasTarget: true
    });
    expect(rowOrderHints(buy, "USDT")).toEqual([{ market: 2, instrumentId: "SOL-USDT-SWAP", side: 1, quantity: "10" }]);
  });

  it("行内下单默认选这个市场持仓最大的候选账户；都没有持仓时交给调用方按原来的选法", () => {
    const [row] = buildCoinRows({
      targets: [
        { instrument_id: "BTC-USDT-SPOT", quantity: "1" },
        { instrument_id: "BTC-USDT-SWAP", quantity: "0" }
      ],
      blocked: [],
      lanes: [],
      swapPositions: [
        { ...swap("BTC-USDT-SWAP", "0.5"), trading_account_id: "A" },
        { ...swap("BTC-USDT-SWAP", "-2"), trading_account_id: "B" }
      ],
      spotHoldings: [],
      converged: false,
      hasTarget: true
    });
    expect(row.memberSwap).toEqual({ A: 0.5, B: -2 });
    // 永续仓位主要在 B 上：平仓差额要下在 B
    expect(largestHolder(row, 2, ["A", "B"])).toBe("B");
    // B 停用或没开通永续（不在候选里）时不选它
    expect(largestHolder(row, 2, ["A", "C"])).toBe("A");
    // 现货没有任何持仓
    expect(largestHolder(row, 1, ["A", "B"])).toBeUndefined();
  });

  it("现货、股票按执行账户分开记数量，持仓一样大时取靠前的候选", () => {
    const [row] = buildCoinRows({
      targets: [{ instrument_id: "600519.XSHG", quantity: "0" }],
      blocked: [],
      lanes: [],
      swapPositions: [],
      spotHoldings: [
        { asset: "600519.XSHG", quantity: "100", instrument_id: "600519.XSHG", trading_account_id: "B" } as Holding,
        { asset: "600519.XSHG", quantity: "100", instrument_id: "600519.XSHG", trading_account_id: "A" } as Holding
      ],
      converged: false,
      hasTarget: true
    });
    expect(row.memberSpot).toEqual({ A: 100, B: 100 });
    expect(largestHolder(row, 3, ["A", "B"])).toBe("A");
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
      lanes: [],
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

  it("股票的结论记在现货一列：不足一手算到位并写明原因；停牌已经在顺延里的不重复写", () => {
    const rowOf = (
      lanes: TargetLane[],
      blocked: { instrument_id: string; quantity: string; reason: string; deferred: boolean }[] = []
    ) =>
      buildCoinRows({
        targets: [{ instrument_id: "600519.XSHG", quantity: "350" }],
        blocked,
        lanes,
        swapPositions: [],
        spotHoldings: [{ asset: "600519.XSHG", quantity: "300", sellable: "300", instrument_id: "600519.XSHG" } as Holding],
        converged: false,
        hasTarget: true
      })[0];
    expect(rowOf([lane("600519.XSHG", 5)])).toMatchObject({ state: "ok", settledReason: "差额不足一手" });
    expect(rowOf([lane("600519.XSHG", 1)])).toMatchObject({ state: "off", offSpot: true });
    expect(rowOf([lane("600519.XSHG", 4, "停牌")])).toMatchObject({ state: "ok", settledReason: "停牌" });
    const deferred = rowOf(
      [lane("600519.XSHG", 4, "停牌")],
      [{ instrument_id: "600519.XSHG", quantity: "50", reason: "停牌", deferred: true }]
    );
    expect(deferred).toMatchObject({ state: "ok", deferredReason: "停牌", settledReason: "" });
  });
});

describe("市值、开仓均价、未实现盈亏", () => {
  const position = (trading_account_id: string, signed_quantity: string, entry_price: string, unrealized_pnl: string) =>
    ({
      instrument_id: "ETH-USDT-SWAP",
      trading_account_id,
      signed_quantity,
      entry_price,
      mark_price: "2000",
      unrealized_pnl,
      market_type: 2
    }) as Position;

  it("市值按标记价取绝对值，均价按持仓数量加权，未实现盈亏相加", () => {
    const [row] = buildCoinRows({
      targets: [],
      blocked: [],
      lanes: [],
      swapPositions: [position("a", "-1", "2100", "100"), position("b", "-3", "1900", "-300")],
      spotHoldings: [],
      converged: false,
      hasTarget: false
    });
    expect(row.value).toBe(8000);
    expect(row.entrySwap).toBe(1950);
    expect(row.unrealizedPnl).toBe(-200);
  });

  it("取不到标记价、均价和盈亏时：市值为 0，均价为 0，盈亏为空", () => {
    const [row] = buildCoinRows({
      targets: [],
      blocked: [],
      lanes: [],
      swapPositions: [swap("BTC-USDT-SWAP", "1")],
      spotHoldings: [],
      converged: false,
      hasTarget: false
    });
    expect(row).toMatchObject({ value: 0, entrySwap: 0, unrealizedPnl: null });
  });

  it("现货持币没有盈亏数（实盘）时，同一个币的整行盈亏显示为空，不只算永续一条腿", () => {
    const [row] = buildCoinRows({
      targets: [],
      blocked: [],
      lanes: [],
      swapPositions: [position("a", "-1", "2100", "100")],
      spotHoldings: [{ asset: "ETH", quantity: "1", instrument_id: "ETH-USDT-SPOT", mark_price: "2000" } as Holding],
      converged: false,
      hasTarget: false
    });
    expect(row.unrealizedPnl).toBeNull();
    expect(row.value).toBe(4000);
  });

  it("只有永续的组合没有现货一侧，页面据此不显示现货列", () => {
    const rows = buildCoinRows({
      targets: [{ instrument_id: "BTC-USDT-SWAP", quantity: "1" }],
      blocked: [],
      lanes: [],
      swapPositions: [],
      spotHoldings: [],
      converged: false,
      hasTarget: true
    });
    expect(hasSpotSide(rows)).toBe(false);
    expect(hasSwapSide(rows)).toBe(true);
    const withSpot = buildCoinRows({
      targets: [],
      blocked: [],
      lanes: [],
      swapPositions: [],
      spotHoldings: [spot("BTC", "1")],
      converged: false,
      hasTarget: false
    });
    expect(hasSpotSide(withSpot)).toBe(true);
    expect(hasSwapSide(withSpot)).toBe(false);
  });
});
