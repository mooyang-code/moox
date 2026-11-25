import { describe, expect, it } from "vitest";
import { marketLabel, marketOfInstrument, marketsTotalText, sortMarkets } from "./markets";

describe("持仓合计说明", () => {
  it("按实际开通的市场写", () => {
    expect(marketsTotalText(["spot"])).toBe("现货合计");
    expect(marketsTotalText(["swap"])).toBe("永续合计");
    expect(marketsTotalText(["spot", "swap"])).toBe("现货和永续合计");
    expect(marketsTotalText(["equity"])).toBe("股票合计");
    expect(marketsTotalText([])).toBe("");
  });
});

describe("市场名称与顺序", () => {
  it("按小写键取中文名，未知值得到 -", () => {
    expect(marketLabel("spot")).toBe("现货");
    expect(marketLabel("swap")).toBe("永续");
    expect(marketLabel("equity")).toBe("股票");
    expect(marketLabel("SWAP")).toBe("-");
    expect(marketLabel("2")).toBe("-");
    expect(marketLabel(undefined)).toBe("-");
  });

  it("按标的 ID 后缀判断市场，永续 BTC-USDT-SWAP 仍是永续", () => {
    expect(marketOfInstrument("BTC-USDT-SWAP")).toBe("swap");
    expect(marketOfInstrument("BTC-USDT-SPOT")).toBe("spot");
    expect(marketOfInstrument("600519.XSHG")).toBe("equity");
  });

  it("市场集合按现货、永续、股票排序", () => {
    expect(sortMarkets(["swap", "spot"])).toEqual(["spot", "swap"]);
  });
});
