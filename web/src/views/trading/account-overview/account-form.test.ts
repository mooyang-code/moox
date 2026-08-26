import { describe, expect, it } from "vitest";
import {
  buildLiveRequest,
  buildPaperSimulationRequest,
  createDefaultAccountForm,
  validateAccountForm,
  type AccountFormModel
} from "./account-form";

const form = (): AccountFormModel => ({
  name: "  Live  ",
  exchange: 1,
  market_type: 1,
  environment: 1,
  credential_secret_id: " secret-1 ",
  execution_mode: 2,
  settlement_asset: "usdt",
  margin_mode: "CROSS",
  initial_balance: "100000",
  maker_fee_rate: "0.001",
  taker_fee_rate: "0.002",
  slippage_bps: "5",
  logical_account_name: "Paper Logical",
  sync_symbols: "BTCUSDT, ETHUSDT"
});

describe("account form builders", () => {
  it("creates a fresh default form for every modal open", () => {
    expect(createDefaultAccountForm()).toEqual({
      name: "",
      exchange: 1,
      market_type: 1,
      execution_mode: 1,
      environment: 1,
      credential_secret_id: "",
      settlement_asset: "USDT",
      margin_mode: "CROSS",
      initial_balance: "100000",
      maker_fee_rate: "0",
      taker_fee_rate: "0",
      slippage_bps: "0",
      logical_account_name: "",
      sync_symbols: ""
    });
  });

  it("rejects invalid Paper values and missing Live symbols", () => {
    const paper = createDefaultAccountForm();
    paper.name = "paper";
    paper.initial_balance = "0";
    expect(validateAccountForm(paper)).toContain("初始资金");

    const live = createDefaultAccountForm();
    live.name = "live";
    live.execution_mode = 2;
    live.credential_secret_id = "secret-1";
    expect(validateAccountForm(live)).toContain("交易标的");
  });

  it("normalizes and deduplicates Live symbols", () => {
    expect(buildLiveRequest({ ...form(), sync_symbols: "btc usdt, BTCUSDT, ETHUSDT" }).sync_symbols).toEqual([
      "BTC",
      "USDT",
      "BTCUSDT",
      "ETHUSDT"
    ]);
  });

  it("does not validate hidden Paper fee fields for Live", () => {
    const live = { ...form(), maker_fee_rate: "invalid", taker_fee_rate: "invalid", slippage_bps: "invalid" };
    expect(validateAccountForm(live)).toBe("");
  });

  it("builds a live oneof request", () => {
    expect(buildLiveRequest(form())).toEqual({
      name: "Live",
      exchange: 1,
      market_type: 1,
      settlement_asset: "USDT",
      margin_mode: "",
      sync_symbols: ["BTCUSDT", "ETHUSDT"],
      live: { environment: 1, credential_secret_id: "secret-1" }
    });
  });

  it("builds a paper request without live fields", () => {
    const request = buildPaperSimulationRequest({ ...form(), execution_mode: 1, credential_secret_id: "must-not-send" });
    expect(request).toEqual({
      account_name: "Live",
      logical_account_name: "Paper Logical",
      exchange: 1,
      market_type: 1,
      settlement_asset: "USDT",
      margin_mode: "",
      initial_balance: "100000",
      maker_fee_rate: "0.001",
      taker_fee_rate: "0.002",
      slippage_bps: "5"
    });
    expect(request).not.toHaveProperty("live");
    expect(request).not.toHaveProperty("credential_secret_id");
    expect(request).not.toHaveProperty("sync_symbols");
  });
});
