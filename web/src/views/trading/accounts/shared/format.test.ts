import { describe, expect, it } from "vitest";
import { fmtPrice, fmtQtyShort } from "./format";

describe("表格里的数量和价格", () => {
  it("数量按大小留位数：一千以上 2 位小数带千分位，一以上 4 位，小于一 4 位有效数字", () => {
    expect(fmtQtyShort("12281.28500935")).toBe("12,281.29");
    expect(fmtQtyShort("346.15261303")).toBe("346.1526");
    expect(fmtQtyShort("9.11226995")).toBe("9.1123");
    expect(fmtQtyShort("-0.000123456")).toBe("-0.0001235");
    expect(fmtQtyShort("38")).toBe("38");
    // 浮点残差算 0，很小的数不写成科学计数法
    expect(fmtQtyShort(0.1 + 0.2 - 0.3)).toBe("0");
    expect(fmtQtyShort("0.00000012")).toBe("0.00000012");
    expect(fmtQtyShort("0")).toBe("0");
    expect(fmtQtyShort("")).toBe("—");
    expect(fmtQtyShort("abc")).toBe("—");
  });

  it("价格：一以上 4 位小数，小于一 5 位有效数字，没有价格显示“—”", () => {
    expect(fmtPrice("103456.789012")).toBe("103,456.789");
    expect(fmtPrice("0.00111234567")).toBe("0.0011123");
    expect(fmtPrice(0)).toBe("—");
    expect(fmtPrice(undefined)).toBe("—");
  });
});
