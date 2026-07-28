import { beforeEach, describe, expect, it, vi } from "vitest";

const { callTrade } = vi.hoisted(() => ({ callTrade: vi.fn() }));
vi.mock("./http", async importOriginal => ({
  ...(await importOriginal<typeof import("./http")>()),
  callTrade
}));

import * as trade from "./index";

describe("Trade public API", () => {
  beforeEach(() => callTrade.mockReset().mockResolvedValue({ ret_info: { code: 0, msg: "ok" } }));

  it("uses exactly two public services", () => {
    expect(trade.tradeServiceMap).toEqual({
      exchangeAccount: "trade_exchange_account",
      execution: "trade_execution"
    });
  });

  it("constructs Exchange account requests", async () => {
    await trade.createAccount({
      name: "paper",
      exchange: 1,
      market_type: 1,
      execution_mode: 1,
      credential_secret_id: "secret-1",
      settlement_asset: "USDT"
    });
    await trade.setLeverage({ exchange_account_id: "account-1", symbol: "BTCUSDT", leverage: "3" });
    await trade.pauseAccount({ exchange_account_id: "account-1", paused: true, reason: "manual" });
    await trade.syncAccount("account-1");

    expect(callTrade).toHaveBeenNthCalledWith(1, "exchangeAccount", "CreateAccount", expect.any(Object));
    expect(callTrade).toHaveBeenNthCalledWith(2, "exchangeAccount", "SetLeverage", {
      exchange_account_id: "account-1",
      symbol: "BTCUSDT",
      leverage: "3"
    });
    expect(callTrade).toHaveBeenNthCalledWith(3, "exchangeAccount", "PauseAccount", {
      exchange_account_id: "account-1",
      paused: true,
      reason: "manual"
    });
    expect(callTrade).toHaveBeenNthCalledWith(4, "exchangeAccount", "SyncAccount", {
      exchange_account_id: "account-1"
    });
  });

  it("constructs MARKET and SWAP LIMIT orders without legacy fields", async () => {
    await trade.placeOrder({
      exchange_account_id: "spot-1",
      client_order_id: "client-market",
      symbol: "BTCUSDT",
      order_type: 1,
      time_in_force: 0,
      side: 1,
      position_side: 0,
      quantity: "0.01",
      reduce_only: false,
      source: "manual"
    });
    await trade.setLeverage({ exchange_account_id: "swap-1", symbol: "BTCUSDT", leverage: "5" });
    await trade.placeOrder({
      exchange_account_id: "swap-1",
      client_order_id: "client-limit",
      symbol: "BTCUSDT",
      order_type: 2,
      time_in_force: 1,
      side: 2,
      position_side: 1,
      quantity: "0.01",
      limit_price: "70000",
      reduce_only: true,
      source: "manual"
    });

    const market = callTrade.mock.calls[0][2];
    expect(market).not.toHaveProperty("limit_price");
    expect(market).toMatchObject({ order_type: 1, time_in_force: 0, position_side: 0 });
    expect(callTrade).toHaveBeenNthCalledWith(2, "exchangeAccount", "SetLeverage", expect.any(Object));
    expect(callTrade).toHaveBeenNthCalledWith(
      3,
      "execution",
      "PlaceOrder",
      expect.objectContaining({ limit_price: "70000", position_side: 1, reduce_only: true })
    );
  });

  it("constructs target, Fill, and Position requests", async () => {
    await trade.submitTarget({
      event_id: "event-1",
      execution_id: "execution-1",
      strategy_run_id: "run-1",
      execution_binding_id: "binding-1",
      exchange_account_id: "account-1",
      command_sequence: "1",
      not_after: "123",
      data_revision: "revision-1",
      targets: [{ instrument_id: "BTC-USDT", symbol: "BTCUSDT", target_quantity: "0.01" }]
    });
    await trade.listFills({ exchange_account_id: "account-1", symbol: "BTCUSDT" });
    await trade.listPositions("account-1", "BTCUSDT");

    expect(callTrade).toHaveBeenNthCalledWith(1, "execution", "SubmitTarget", expect.any(Object));
    expect(callTrade).toHaveBeenNthCalledWith(2, "execution", "ListFills", {
      exchange_account_id: "account-1",
      symbol: "BTCUSDT"
    });
    expect(callTrade).toHaveBeenNthCalledWith(3, "execution", "ListPositions", {
      exchange_account_id: "account-1",
      symbol: "BTCUSDT"
    });
  });

  it("does not export obsolete service operations", () => {
    for (const name of [
      "deleteAccount",
      "syncBalances",
      "transfer",
      "convertDust",
      "createApiKey",
      "createChannel",
      "syncOrders",
      "syncTrades",
      "syncPositions",
      "startRebalance",
      "getSaga"
    ]) {
      expect(trade).not.toHaveProperty(name);
    }
  });

  it("only exposes cancel for states accepted by the Order aggregate", () => {
    for (const state of ["OPEN", "PARTIALLY_FILLED", "SUBMIT_UNKNOWN"]) {
      expect(trade.canCancelOrderState(state)).toBe(true);
    }
    for (const state of ["FILLED", "CANCELED", "PARTIALLY_CANCELED", "REJECTED", "EXPIRED", "CANCELING"]) {
      expect(trade.canCancelOrderState(state)).toBe(false);
    }
  });
});
