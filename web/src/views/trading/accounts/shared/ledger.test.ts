import { describe, expect, it } from "vitest";
import { amountClass, defaultLedgerRange, ledgerRangeError, ledgerTypeLabel } from "./ledger";
import { flattenStatusText } from "./flatten";

describe("account ledger helpers", () => {
  it("labels ledger types and falls back to the raw value", () => {
    expect(ledgerTypeLabel("FUNDING_FEE")).toBe("资金费");
    expect(ledgerTypeLabel("NEW_TYPE")).toBe("NEW_TYPE");
  });

  it("defaults to the last seven days", () => {
    expect(defaultLedgerRange(8 * 86_400_000)).toEqual([86_400_000, 8 * 86_400_000]);
  });

  it("rejects ranges longer than 90 days", () => {
    expect(ledgerRangeError([0, 91 * 86_400_000])).toContain("90");
    expect(ledgerRangeError([0, 7 * 86_400_000])).toBe("");
    expect(ledgerRangeError([10, 5])).not.toBe("");
  });

  it("colors amounts by sign", () => {
    expect(amountClass("1.5")).toBe("tx-pos");
    expect(amountClass("-0.01")).toBe("tx-neg");
    expect(amountClass("0")).toBe("");
  });

  it("describes flatten progress", () => {
    expect(flattenStatusText("PARTIAL")).toContain("部分完成");
    expect(flattenStatusText("COMPLETED")).toBe("平仓完成");
  });
});

describe("default ledger market", () => {
  it("多市场账户默认查永续，只开通现货时用现货", async () => {
    const { defaultLedgerMarket } = await import("./ledger");
    expect(defaultLedgerMarket([1, 2])).toBe(2);
    expect(defaultLedgerMarket([1])).toBe(1);
    expect(defaultLedgerMarket([])).toBe(1);
  });
});
