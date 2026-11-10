import { describe, expect, it } from "vitest";
import { marketsTotalText } from "./markets";

describe("持仓合计说明", () => {
  it("按实际开通的市场写", () => {
    expect(marketsTotalText([1])).toBe("现货合计");
    expect(marketsTotalText([2])).toBe("永续合计");
    expect(marketsTotalText([1, 2])).toBe("现货和永续合计");
    expect(marketsTotalText([3])).toBe("股票合计");
  });
});
