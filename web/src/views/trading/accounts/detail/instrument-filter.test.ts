import { describe, expect, it } from "vitest";
import { instrumentFilter } from "./instrument-filter";

describe("标的筛选补全", () => {
  it("币补计价币和市场后缀，交易对只补市场后缀，完整 ID 原样使用", () => {
    expect(instrumentFilter(" btc ", 2, "USDT")).toBe("BTC-USDT-SWAP");
    expect(instrumentFilter("eth-usdt", 1, "USDT")).toBe("ETH-USDT-SPOT");
    expect(instrumentFilter("BTC-USDT-SWAP", 1, "USDT")).toBe("BTC-USDT-SWAP");
  });

  it("没有输入时不筛选；没有市场时只补计价币", () => {
    expect(instrumentFilter("  ", 2, "USDT")).toBeUndefined();
    expect(instrumentFilter("SOL", undefined, "USDT")).toBe("SOL-USDT");
  });

  it("股票代码原样返回：带点的输入、市场为股票、没选市场都只去空格和大写", () => {
    expect(instrumentFilter("600519.xshg", 3, "CNY")).toBe("600519.XSHG");
    expect(instrumentFilter("600519.xshg", undefined, "CNY")).toBe("600519.XSHG");
    expect(instrumentFilter(" 600519 ", 3, "CNY")).toBe("600519");
    expect(instrumentFilter("600519.xshg", 1, "USDT")).toBe("600519.XSHG");
  });
});
