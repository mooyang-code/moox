import { describe, expect, it } from "vitest";
import { amountClass, defaultLedgerRange, ledgerTypeLabel } from "./account-ledger";
import { flattenStatusText } from "./account-flatten";

describe("account ledger helpers", () => {
  it("labels ledger types and falls back to the raw value", () => {
    expect(ledgerTypeLabel("FUNDING_FEE")).toBe("资金费");
    expect(ledgerTypeLabel("NEW_TYPE")).toBe("NEW_TYPE");
  });

  it("defaults to the last seven days", () => {
    expect(defaultLedgerRange(8 * 86_400_000)).toEqual([86_400_000, 8 * 86_400_000]);
  });

  it("colors amounts by sign", () => {
    expect(amountClass("1.5")).toBe("amount-positive");
    expect(amountClass("-0.01")).toBe("amount-negative");
    expect(amountClass("0")).toBe("");
  });

  it("describes flatten progress", () => {
    expect(flattenStatusText("PARTIAL")).toContain("部分完成");
    expect(flattenStatusText("COMPLETED")).toBe("平仓完成");
  });
});
