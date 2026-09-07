import { beforeEach, describe, expect, it, vi } from "vitest";

const { callTrade } = vi.hoisted(() => ({ callTrade: vi.fn() }));
vi.mock("./http", async importOriginal => ({
  ...(await importOriginal<typeof import("./http")>()),
  callTrade
}));

import * as trade from "./index";

describe("Trade public API", () => {
  beforeEach(() => callTrade.mockReset().mockResolvedValue({ ret_info: { code: 0, msg: "ok" } }));

  it("registers the unified Trade console service", () => {
    expect(trade.tradeServiceMap).toEqual({ console: "trade_console" });
  });

  it("constructs 组合账户生命周期请求", async () => {
    await trade.createLogicalAccount({ name: "paper", execution_mode: 1, market_type: 1, settlement_asset: "USDT" });
    await trade.pauseLogicalAccount("logical-1", "manual intervention");
    await trade.resumeLogicalAccount("logical-1");
    await trade.flattenLogicalAccount("action-1", "logical-1", "risk cleanup");
    expect(callTrade).toHaveBeenNthCalledWith(1, "console", "CreateLogicalAccount", expect.any(Object));
    expect(callTrade).toHaveBeenNthCalledWith(2, "console", "PauseLogicalAccount", {
      logical_account_id: "logical-1",
      reason: "manual intervention"
    });
    expect(callTrade).toHaveBeenNthCalledWith(3, "console", "ResumeLogicalAccount", {
      logical_account_id: "logical-1"
    });
    expect(callTrade).toHaveBeenNthCalledWith(4, "console", "FlattenLogicalAccount", {
      action_id: "action-1",
      logical_account_id: "logical-1",
      reason: "risk cleanup"
    });
  });

  it("uses canonical unified account RPC names", async () => {
    await trade.listTradingAccounts({ page: { page: 1, size: 20 } });
    expect(callTrade).toHaveBeenLastCalledWith("console", "ListTradingAccounts", {
      page: { page: 1, size: 20 }
    });

    await trade.syncTradingAccount("account-1");
    expect(callTrade).toHaveBeenLastCalledWith("console", "SyncTradingAccount", {
      trading_account_id: "account-1"
    });
  });

  it("keeps Paper execution mode separate from AccountEnvironment", () => {
    expect(trade.executionModeLabels).toEqual({ 0: "-", 1: "Paper", 2: "Live" });
    expect(trade.environmentLabels).toEqual({ 0: "-", 1: "Testnet", 2: "Production" });
  });

  it("preserves explicit account control mode in creation requests", async () => {
    const logical = { name: "manual", execution_mode: 1 as const, market_type: 1 as const, settlement_asset: "USDT", control_mode: 2 as const };
    await trade.createLogicalAccount(logical);
    expect(callTrade).toHaveBeenLastCalledWith("console", "CreateLogicalAccount", logical);
    const paper = { account_name: "paper", logical_account_name: "manual", exchange: 1 as const, market_type: 1 as const, settlement_asset: "USDT", initial_balance: "1000", maker_fee_rate: "0", taker_fee_rate: "0", slippage_bps: "0", control_mode: 2 as const };
    await trade.createPaperSimulation(paper);
    expect(callTrade).toHaveBeenLastCalledWith("console", "CreatePaperSimulation", paper);
  });

  it("exposes canonical Chinese order state labels", () => {
    expect(trade.orderStateLabels).toEqual({
      PENDING: "等待提交",
      SUBMITTING: "提交中",
      SUBMIT_UNKNOWN: "提交状态未知",
      OPEN: "挂单中",
      PARTIALLY_FILLED: "部分成交",
      CANCELING: "撤单中",
      CANCEL_UNKNOWN: "撤单状态未知",
      FILLED: "已成交",
      CANCELED: "已撤销",
      PARTIALLY_CANCELED: "部分撤销",
      REJECTED: "已拒绝",
      EXPIRED: "已过期"
    });
  });

  it("keeps manual ownership fields server-controlled", async () => {
    await trade.placeManualOrder({
      action_id: "action-1",
      trading_account_id: "account-1",
      client_order_id: "client1",
      instrument_id: "instrument-1",
      order_type: 1,
      fill_policy: 0,
      side: 2,
      position_side: 1,
      quantity: "0.01",
      reason: "manual risk reduction"
    });
    const request = callTrade.mock.calls[0][2];
    expect(request).toEqual({
      action_id: "action-1",
      trading_account_id: "account-1",
      client_order_id: "client1",
      instrument_id: "instrument-1",
      order_type: 1,
      fill_policy: 0,
      side: 2,
      position_side: 1,
      quantity: "0.01",
      reason: "manual risk reduction"
    });
    expect(request).not.toHaveProperty("source");
    expect(request).not.toHaveProperty("owner_id");
    expect(request).not.toHaveProperty("reduce_position_only");
  });

  it("uses ordinary admission without the takeover endpoint", async () => {
    const request = {
      action_id: "ordinary-1", logical_account_id: "manual-1", trading_account_id: "paper-1",
      client_order_id: "client-1", instrument_id: "BTCUSDT", order_type: 1 as const,
      fill_policy: 0 as const, side: 1 as const, position_side: 0 as const,
      quantity: "0.01", reason: "ordinary", deadline_at: "2000000000000"
    };
    await trade.submitOrder(request);
    expect(callTrade).toHaveBeenCalledTimes(1);
    expect(callTrade).toHaveBeenCalledWith("console", "SubmitOrder", request);
  });

  it("reads current quantity targets and parses per-account flatten progress", async () => {
    await trade.getLogicalAccountTarget("logical-1");
    expect(callTrade).toHaveBeenCalledWith("console", "GetLogicalAccountTarget", {
      logical_account_id: "logical-1"
    });
    expect(
      trade.parseFlattenResult({
        action_id: "action-1",
        logical_account_id: "logical-1",
        action_type: "FLATTEN",
        reason: "risk",
        status: "PARTIAL",
        result_json: JSON.stringify({
          accounts: [
            {
              trading_account_id: "account-1",
              status: "PARTIAL",
              remaining_positions: [{ instrument_id: "instrument-1", quantity: "0.1", reason: "order rejected" }]
            }
          ]
        }),
        last_error: "",
        created_at: "1",
        updated_at: "2"
      })?.accounts[0].remaining_positions?.[0].quantity
    ).toBe("0.1");
  });
});
