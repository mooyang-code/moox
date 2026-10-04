import { describe, expect, it } from "vitest";
import type { TradingAccount } from "@/api/trade/types";
import { accountEnvironmentView, accountStatusView, snapshotPnlClass, snapshotValue } from "./account-display";

const account = (overrides: Partial<TradingAccount> = {}): TradingAccount => ({
  trading_account_id: "ta-1",
  space_id: "space-1",
  name: "account",
  exchange: 1,
  market_type: 1,
  execution_mode: 2,
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
  sync_symbols: ["BTCUSDT"],
  live: { environment: 1, credential_secret_id: "secret-1" },
  ...overrides
});

describe("account display helpers", () => {
  it("maps status and readiness without treating enabled as ready", () => {
    expect(accountStatusView("ENABLED", false)).toEqual({ label: "未就绪", color: "orange" });
    expect(accountStatusView("ENABLED", true)).toEqual({ label: "就绪", color: "green" });
    expect(accountStatusView("ERROR", false).color).toBe("red");
    expect(accountStatusView("UNKNOWN", false).color).toBe("gray");
  });

  it("keeps simulated accounts separate from real environment labels", () => {
    expect(
      accountEnvironmentView(
        account({
          execution_mode: 1,
          paper: { initial_balance: "1", maker_fee_rate: "0", taker_fee_rate: "0", slippage_bps: "0" },
          live: undefined
        })
      )
    ).toBe("模拟环境");
    expect(accountEnvironmentView(account({ live: { environment: 2, credential_secret_id: "secret-1" } }))).toBe("生产环境");
  });

  it("formats empty snapshot values and PnL classes", () => {
    expect(snapshotValue("")).toBe("-");
    expect(snapshotPnlClass("1.2")).toBe("positive");
    expect(snapshotPnlClass("-1.2")).toBe("negative");
    expect(snapshotPnlClass("0")).toBe("");
  });
});
