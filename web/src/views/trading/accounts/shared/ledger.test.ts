import { describe, expect, it } from "vitest";
import { amountClass, defaultLedgerRange, ledgerRangeError, ledgerTypeLabel } from "./ledger";

describe("account ledger helpers", () => {
  it("labels ledger types and falls back to the raw value", () => {
    expect(ledgerTypeLabel("FUNDING_FEE")).toBe("资金费");
    expect(ledgerTypeLabel("NEW_TYPE")).toBe("NEW_TYPE");
  });

  it("defaults to the last seven whole days in the display zone, today included", () => {
    const now = Date.parse("2026-11-07T20:00:00Z");
    // UTC：11-01 零点到 11-08 零点
    expect(defaultLedgerRange("UTC", now)).toEqual([Date.parse("2026-11-01T00:00:00Z"), Date.parse("2026-11-08T00:00:00Z")]);
    // 北京时间已经是 11-08：11-02 零点到 11-09 零点
    expect(defaultLedgerRange("Asia/Shanghai", now)).toEqual([
      Date.parse("2026-11-01T16:00:00Z"),
      Date.parse("2026-11-08T16:00:00Z")
    ]);
  });

  it("rejects ranges longer than 90 days", () => {
    expect(ledgerRangeError([0, 91 * 86_400_000])).toContain("90");
    expect(ledgerRangeError([0, 7 * 86_400_000])).toBe("");
    expect(ledgerRangeError([10, 5])).not.toBe("");
    expect(ledgerRangeError(null)).not.toBe("");
  });

  it("colors amounts by sign", () => {
    expect(amountClass("1.5")).toBe("tx-pos");
    expect(amountClass("-0.01")).toBe("tx-neg");
    expect(amountClass("0")).toBe("");
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
