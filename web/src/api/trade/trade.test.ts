import { beforeEach, describe, expect, it, vi } from "vitest";

const { callTrade } = vi.hoisted(() => ({ callTrade: vi.fn() }));
vi.mock("./http", async importOriginal => ({
  ...(await importOriginal<typeof import("./http")>()),
  callTrade
}));

import * as trade from "./index";

describe("Trade public API", () => {
  beforeEach(() => callTrade.mockReset().mockResolvedValue({ ret_info: { code: 0, msg: "ok" } }));

  it("registers the three real Trade services", () => {
    expect(trade.tradeServiceMap).toEqual({
      exchangeAccount: "trade_exchange_account",
      execution: "trade_execution",
      logicalAccount: "trade_logical_account"
    });
  });

  it("constructs Logical Account lifecycle requests", async () => {
    await trade.createLogicalAccount({ name: "paper", execution_mode: 1, market_type: 1, settlement_asset: "USDT" });
    await trade.pauseLogicalAccount("logical-1", "manual intervention");
    await trade.resumeLogicalAccount("logical-1");
    await trade.flattenLogicalAccount("action-1", "logical-1", "risk cleanup");
    expect(callTrade).toHaveBeenNthCalledWith(1, "logicalAccount", "CreateLogicalAccount", expect.any(Object));
    expect(callTrade).toHaveBeenNthCalledWith(2, "logicalAccount", "PauseLogicalAccount", {
      logical_account_id: "logical-1",
      reason: "manual intervention"
    });
    expect(callTrade).toHaveBeenNthCalledWith(3, "logicalAccount", "ResumeLogicalAccount", {
      logical_account_id: "logical-1"
    });
    expect(callTrade).toHaveBeenNthCalledWith(4, "logicalAccount", "FlattenLogicalAccount", {
      action_id: "action-1",
      logical_account_id: "logical-1",
      reason: "risk cleanup"
    });
  });

  it("keeps manual ownership fields server-controlled", async () => {
    await trade.placeManualOrder({
      action_id: "action-1",
      exchange_account_id: "account-1",
      client_order_id: "client1",
      symbol: "BTC-USDT-SWAP",
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
      exchange_account_id: "account-1",
      client_order_id: "client1",
      symbol: "BTC-USDT-SWAP",
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

  it("reads current quantity targets and parses per-account flatten progress", async () => {
    await trade.getLogicalAccountTarget("logical-1");
    expect(callTrade).toHaveBeenCalledWith("execution", "GetLogicalAccountTarget", {
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
              exchange_account_id: "account-1",
              status: "PARTIAL",
              remaining_positions: [{ symbol: "BTC-USDT-SWAP", quantity: "0.1", reason: "order rejected" }]
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
