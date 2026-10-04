import { describe, expect, it } from "vitest";
import type { TradingAccount } from "@/api/trade/types";
import { accountModeFromQuery, accountModeTabs, accountModeToExecutionMode, accountTypeLabel } from "./account-mode";

const account = (execution_mode: TradingAccount["execution_mode"]): TradingAccount => ({
  trading_account_id: "ta-1",
  space_id: "space-1",
  name: "account",
  exchange: 1,
  market_type: 1,
  execution_mode,
  settlement_asset: "USDT",
  margin_mode: "CROSS",
  status: "ENABLED",
  ready: true,
  leverage_settings: {},
  last_sync_at: "",
  last_ready_at: "",
  last_error: "",
  created_at: "",
  updated_at: "",
  sync_symbols: []
});

describe("account mode helpers", () => {
  it("normalizes route values and maps server execution modes", () => {
    expect(accountModeFromQuery(undefined)).toBe("all");
    expect(accountModeFromQuery("unknown")).toBe("all");
    expect(accountModeFromQuery("live")).toBe("live");
    expect(accountModeFromQuery("paper")).toBe("paper");
    expect(accountModeToExecutionMode("all")).toBeUndefined();
    expect(accountModeToExecutionMode("live")).toBe(2);
    expect(accountModeToExecutionMode("paper")).toBe(1);
  });

  it("exposes Chinese tab and account type labels", () => {
    expect(accountModeTabs).toEqual([
      { key: "all", label: "全部" },
      { key: "live", label: "真实账户" },
      { key: "paper", label: "模拟账户" }
    ]);
    expect(accountTypeLabel(account(1))).toBe("模拟账户");
    expect(accountTypeLabel(account(2))).toBe("真实账户");
    expect(accountTypeLabel(account(0))).toBe("未知账户");
  });
});
