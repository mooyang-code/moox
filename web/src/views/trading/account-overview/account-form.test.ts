import { describe, expect, it } from "vitest";
import { buildLiveRequest, buildPaperSimulationRequest, type AccountFormModel } from "./account-form";

const form = (): AccountFormModel => ({
  name: "  Live  ", exchange: 1, market_type: 1, environment: 1, credential_secret_id: " secret-1 ",
  settlement_asset: "usdt", margin_mode: "CROSS", initial_balance: "100000", maker_fee_rate: "0.001",
  taker_fee_rate: "0.002", slippage_bps: "5", logical_account_name: "Paper Logical", sync_symbols: "BTCUSDT, ETHUSDT"
});

describe("account form builders", () => {
  it("builds a live oneof request", () => {
    expect(buildLiveRequest(form())).toEqual({
      name: "Live", exchange: 1, market_type: 1, settlement_asset: "USDT", margin_mode: "",
      sync_symbols: ["BTCUSDT", "ETHUSDT"], live: { environment: 1, credential_secret_id: "secret-1" }
    });
  });

  it("builds a paper request without live fields", () => {
    expect(buildPaperSimulationRequest(form())).toEqual({
      account_name: "Live", logical_account_name: "Paper Logical", exchange: 1, market_type: 1,
      settlement_asset: "USDT", margin_mode: "", initial_balance: "100000", maker_fee_rate: "0.001",
      taker_fee_rate: "0.002", slippage_bps: "5"
    });
  });
});
