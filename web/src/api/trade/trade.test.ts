import { beforeEach, describe, expect, it, vi } from "vitest";

const { callTrade } = vi.hoisted(() => ({ callTrade: vi.fn() }));
vi.mock("./http", async importOriginal => ({
  ...(await importOriginal<typeof import("./http")>()),
  callTrade
}));

import * as trade from "./index";

describe("Trade public API", () => {
  beforeEach(() => callTrade.mockReset().mockResolvedValue({ ret_info: { code: 0, msg: "ok" } }));

  it("constructs 组合账户生命周期请求", async () => {
    await trade.createLogicalAccount({ name: "paper", execution_mode: 1, settlement_asset: "USDT" });
    await trade.pauseLogicalAccount("logical-1", "manual intervention");
    await trade.resumeLogicalAccount("logical-1");
    await trade.flattenLogicalAccount("action-1", "logical-1", "risk cleanup");
    expect(callTrade).toHaveBeenNthCalledWith(1, "console", "CreateLogicalAccount", expect.any(Object), { silent: true });
    expect(callTrade).toHaveBeenNthCalledWith(
      2,
      "console",
      "PauseLogicalAccount",
      {
        logical_account_id: "logical-1",
        reason: "manual intervention"
      },
      { silent: true }
    );
    expect(callTrade).toHaveBeenNthCalledWith(
      3,
      "console",
      "ResumeLogicalAccount",
      { logical_account_id: "logical-1" },
      { silent: true }
    );
    expect(callTrade).toHaveBeenNthCalledWith(
      4,
      "console",
      "FlattenLogicalAccount",
      {
        action_id: "action-1",
        logical_account_id: "logical-1",
        reason: "risk cleanup"
      },
      { silent: true }
    );
  });

  it("uses canonical unified account RPC names", async () => {
    await trade.listTradingAccounts({ page: { page: 1, size: 20 } });
    expect(callTrade).toHaveBeenLastCalledWith("console", "ListTradingAccounts", { page: { page: 1, size: 20 } }, {});

    await trade.syncTradingAccount("account-1");
    expect(callTrade).toHaveBeenLastCalledWith("console", "SyncTradingAccount", {
      trading_account_id: "account-1"
    });
  });

  it("preserves explicit account control mode in creation requests", async () => {
    const logical = {
      name: "manual",
      execution_mode: 1 as const,
      settlement_asset: "USDT",
      control_mode: 2 as const
    };
    await trade.createLogicalAccount(logical);
    expect(callTrade).toHaveBeenLastCalledWith("console", "CreateLogicalAccount", logical, { silent: true });
    const paper = {
      account_name: "paper",
      logical_account_name: "manual",
      exchange: 1 as const,
      markets: [1, 2] as (1 | 2)[],
      settlement_asset: "USDT",
      initial_balance: "1000",
      maker_fee_rate: "0",
      taker_fee_rate: "0",
      slippage_bps: "0",
      control_mode: 2 as const
    };
    await trade.createPaperSimulation(paper);
    expect(callTrade).toHaveBeenLastCalledWith("console", "CreatePaperSimulation", paper, { silent: true });
  });

  it("文案键与后端状态一一对应", () => {
    // 后端常量：target/executor.go 的 Status*，operator 的 Action 状态。
    expect(Object.keys(trade.targetStateLabels).sort()).toEqual(["BLOCKED", "CONVERGED", "CONVERGING", "EXPIRED", "PENDING"]);
    expect(Object.keys(trade.actionStateLabels).sort()).toEqual(["COMPLETED", "FAILED", "PARTIAL", "RUNNING"]);
    expect(Object.keys(trade.orderOwnerLabels).sort()).toEqual(["EXTERNAL", "OPERATOR", "SYSTEM", "TARGET"]);
    expect(Object.keys(trade.operatorActionTypeLabels).sort()).toEqual([
      "CANCEL_ORDER",
      "DELIST_FLATTEN",
      "FLATTEN",
      "MANUAL_ORDER",
      "REDUCE",
      "SUBMIT_ORDER"
    ]);
  });

  it("撤单中和撤单状态未知可以重试确认", () => {
    expect(trade.canCancelOrderState("CANCELING")).toBe(true);
    expect(trade.canCancelOrderState("CANCEL_UNKNOWN")).toBe(true);
    expect(trade.canCancelOrderState("FILLED")).toBe(false);
    // 未完成订单比可撤单多“等待提交”“提交中”：订单页签据此决定是否后台刷新
    expect(trade.isActiveOrderState("PENDING")).toBe(true);
    expect(trade.isActiveOrderState("SUBMITTING")).toBe(true);
    expect(trade.canCancelOrderState("PENDING")).toBe(false);
    expect(trade.isActiveOrderState("FILLED")).toBe(false);
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
      action_id: "ordinary-1",
      logical_account_id: "manual-1",
      trading_account_id: "paper-1",
      client_order_id: "client-1",
      instrument_id: "BTCUSDT",
      order_type: 1 as const,
      fill_policy: 0 as const,
      side: 1 as const,
      position_side: 0 as const,
      quantity: "0.01",
      reason: "ordinary",
      deadline_at: "2000000000000"
    };
    await trade.submitOrder(request);
    expect(callTrade).toHaveBeenCalledTimes(1);
    expect(callTrade).toHaveBeenCalledWith("console", "SubmitOrder", request, { silent: true });
  });

  it("reads current quantity targets and parses per-account flatten progress", async () => {
    await trade.getLogicalAccountTarget("logical-1");
    expect(callTrade).toHaveBeenCalledWith("console", "GetLogicalAccountTarget", { logical_account_id: "logical-1" }, {});
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

  it("管理台不暴露只给事件中心用的写入接口", () => {
    expect(trade).not.toHaveProperty("blockInstrument");
    expect(trade).not.toHaveProperty("flattenInstrument");
  });

  it("下架名单只暴露读取和解除：读取不带参数，解除按（交易所、市场、代码）定位", async () => {
    await trade.listBlockedInstruments({ silent: true });
    expect(callTrade).toHaveBeenLastCalledWith("console", "ListBlockedInstruments", {}, { silent: true });
    const req = { exchange: 1 as const, market_type: 1 as const, exchange_symbol: "LUNAUSDT" };
    await trade.unblockInstrument(req, { silent: true });
    expect(callTrade).toHaveBeenLastCalledWith("console", "UnblockInstrument", req, { silent: true });
  });

  it("收益拆解、操作记录和按来源筛选订单的请求原样透传", async () => {
    const pnl = { logical_account_id: "logical-1", start_time: "1000", end_time: "2000" };
    await trade.summarizeAccountPnl(pnl);
    expect(callTrade).toHaveBeenLastCalledWith("console", "SummarizeAccountPnl", pnl);

    const actions = {
      logical_account_id: "logical-1",
      action_type: "REDUCE" as const,
      page: { page: 2, size: 10 }
    };
    await trade.listOperatorActions(actions);
    expect(callTrade).toHaveBeenLastCalledWith("console", "ListOperatorActions", actions, {});

    await trade.listOrders({ logical_account_id: "logical-1", owner_type: "SYSTEM" });
    expect(callTrade).toHaveBeenLastCalledWith(
      "console",
      "ListOrders",
      {
        logical_account_id: "logical-1",
        owner_type: "SYSTEM"
      },
      {}
    );
  });
});
