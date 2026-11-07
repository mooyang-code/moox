import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ callControl: vi.fn(), callTrade: vi.fn() }));
vi.mock("@/api/admin/http", () => ({ callControl: mocks.callControl }));
vi.mock("@/api/trade/http", () => ({ callTrade: mocks.callTrade }));

import {
  createEventRule,
  enumName,
  getEventExecution,
  listEventExecutions,
  listEventRules,
  listSubscribableEvents,
  retryEventExecution,
  updateEventRule
} from "./index";
import { buildHoldersRequest, listInstrumentHolders, registerDelistNotice } from "./delist";

describe("事件中心接口", () => {
  beforeEach(() => {
    mocks.callControl.mockReset();
    mocks.callTrade.mockReset();
  });

  it("枚举数字转成名称，未知值落到未指定", () => {
    const names = ["A_UNSPECIFIED", "A_ONE", "A_TWO"] as const;
    expect(enumName(1, names)).toBe("A_ONE");
    expect(enumName("2", names)).toBe("A_TWO");
    expect(enumName("A_TWO", names)).toBe("A_TWO");
    expect(enumName(9, names)).toBe("A_UNSPECIFIED");
    expect(enumName(undefined, names)).toBe("A_UNSPECIFIED");
  });

  it("事件目录带上已上线的系统动作，没上线时是空列表", async () => {
    mocks.callControl.mockResolvedValue({ events: [{ event_name: "e.a", version: 1, title: "t" }] });
    expect(await listSubscribableEvents()).toMatchObject({ availableSystemActions: [] });

    mocks.callControl.mockResolvedValue({ events: [], available_system_actions: [1] });
    expect((await listSubscribableEvents()).availableSystemActions).toEqual(["SYSTEM_ACTION_SELL_DELISTED_HOLDINGS"]);
  });

  it("规则列表：枚举、缺省的布尔和范围都被规范化", async () => {
    mocks.callControl.mockResolvedValue({
      rules: [
        {
          rule_id: "r1",
          name: "系统规则",
          event_name: "event.collector.instrument.delist_scheduled",
          action_type: 2,
          system_action: 1,
          system: true,
          enabled: true,
          created_at_ms: "1700000000000",
          last_execution: { status: 3, at_ms: 5 }
        },
        {
          rule_id: "r2",
          name: "通知",
          action_type: 1,
          notify_url: "https://a.example/h",
          scope: [{ field: "exchange", values: ["okx"] }]
        }
      ],
      page_result: { total: 2 }
    });
    const rsp = await listEventRules({ page: 1, size: 20 });
    expect(mocks.callControl).toHaveBeenCalledWith("callback-dispatcher", "ListEventRules", { page: { page: 1, size: 20 } });
    expect(rsp.rules[0]).toMatchObject({
      action_type: "ACTION_TYPE_SYSTEM",
      system_action: "SYSTEM_ACTION_SELL_DELISTED_HOLDINGS",
      system: true,
      created_at_ms: 1700000000000,
      last_execution: { status: "EXECUTION_STATUS_SUCCEEDED", at_ms: 5 }
    });
    expect(rsp.rules[1]).toMatchObject({
      enabled: false,
      system: false,
      allow_private_address: false,
      scope: [{ field: "exchange", values: ["okx"] }]
    });
    expect(rsp.rules[1].last_execution).toBeUndefined();
  });

  it("创建规则返回只显示一次的验证密钥", async () => {
    mocks.callControl.mockResolvedValue({ rule: { rule_id: "r1", action_type: 1 }, signing_secret: "s3cret" });
    const result = await createEventRule({
      name: "n",
      event_name: "e",
      scope: [],
      notify_url: "https://a.example/h",
      allow_private_address: false
    });
    expect(result.signing_secret).toBe("s3cret");
    expect(mocks.callControl).toHaveBeenCalledWith(
      "callback-dispatcher",
      "CreateEventRule",
      expect.objectContaining({ name: "n" })
    );
  });

  it("只启停时请求里只带 rule_id 和 enabled", async () => {
    mocks.callControl.mockResolvedValue({ rule: { rule_id: "r1" } });
    await updateEventRule({ rule_id: "r1", enabled: false });
    expect(mocks.callControl).toHaveBeenCalledWith("callback-dispatcher", "UpdateEventRule", { rule_id: "r1", enabled: false });
  });

  it("执行记录列表带筛选和分页", async () => {
    mocks.callControl.mockResolvedValue({
      executions: [{ execution_id: "x1", status: 4, action_type: 1, attempts: "3", acknowledged: true }],
      page_result: { total: 1 }
    });
    const rsp = await listEventExecutions({ rule_id: "r1", status: "EXECUTION_STATUS_FAILED", page: { page: 2, size: 20 } });
    expect(mocks.callControl).toHaveBeenCalledWith("callback-dispatcher", "ListEventExecutions", {
      rule_id: "r1",
      status: "EXECUTION_STATUS_FAILED",
      page: { page: 2, size: 20 }
    });
    expect(rsp.executions[0]).toMatchObject({ status: "EXECUTION_STATUS_FAILED", attempts: 3, acknowledged: true });
  });

  it("执行详情包含每次尝试和下架各步", async () => {
    mocks.callControl.mockResolvedValue({
      detail: {
        execution: { execution_id: "x1", status: 3 },
        payload_json: "{}",
        attempts: [{ attempt: 1, http_status: 500, duration_ms: "120", error: "" }],
        delist_steps: [{ round: 1, kind: 2, logical_account_id: "la-1", status: "SUCCEEDED" }]
      }
    });
    const detail = await getEventExecution("x1");
    expect(detail.attempts[0]).toMatchObject({ http_status: 500, duration_ms: 120 });
    expect(detail.delist_steps[0]).toMatchObject({ round: 1, kind: "DELIST_STEP_KIND_FLATTEN" });
  });

  it("重新执行调用 RetryEventExecution", async () => {
    mocks.callControl.mockResolvedValue({ execution: { execution_id: "x1", status: 1 } });
    const result = await retryEventExecution("x1");
    expect(mocks.callControl).toHaveBeenCalledWith("callback-dispatcher", "RetryEventExecution", { execution_id: "x1" });
    expect(result.status).toBe("EXECUTION_STATUS_PENDING");
  });
});

describe("手工登记下架的外部接口", () => {
  beforeEach(() => {
    mocks.callControl.mockReset();
    mocks.callTrade.mockReset();
  });

  it("预览请求把交易所和市场换成 Trade 的枚举名", () => {
    expect(
      buildHoldersRequest({ exchange: "okx", market: "SWAP", exchange_symbol: " FOO-USDT-SWAP ", paper_only: true })
    ).toEqual({
      exchange: "EXCHANGE_OKX",
      market_type: "MARKET_TYPE_SWAP",
      exchange_symbol: "FOO-USDT-SWAP",
      paper_only: true
    });
  });

  it("预览持有者：静默调用，缺省字段补默认值", async () => {
    mocks.callTrade.mockResolvedValue({
      ret_info: { code: 0 },
      holders: [{ space_id: "crypto", logical_account_id: "la-1", quantity: 2 }]
    });
    const holders = await listInstrumentHolders({
      exchange: "binance",
      market: "SPOT",
      exchange_symbol: "FOOUSDT",
      paper_only: false
    });
    expect(mocks.callTrade).toHaveBeenCalledWith(
      "console",
      "ListInstrumentHolders",
      { exchange: "EXCHANGE_BINANCE", market_type: "MARKET_TYPE_SPOT", exchange_symbol: "FOOUSDT", paper_only: false },
      { silent: true }
    );
    expect(holders).toEqual({
      holders: [
        { space_id: "crypto", logical_account_id: "la-1", trading_account_id: "", quantity: "2", dust: false, paper: false }
      ],
      verified: true,
      instrumentUnknown: false
    });
  });

  it("只查模拟盘时，返回的每一项都按模拟盘处理", async () => {
    mocks.callTrade.mockResolvedValue({
      ret_info: { code: 0 },
      holders: [{ space_id: "crypto", logical_account_id: "la-1", paper: false }]
    });
    const preview = await listInstrumentHolders({
      exchange: "okx",
      market: "SWAP",
      exchange_symbol: "FOO-USDT-SWAP",
      paper_only: true
    });
    expect(preview.holders.every(item => item.paper)).toBe(true);
  });

  it("没有 holders 字段又没有无此标的标记时，不能当成没人持有", async () => {
    const input = { exchange: "binance", market: "SPOT", exchange_symbol: "FOOUSDT", paper_only: false } as const;
    mocks.callTrade.mockResolvedValue({ ret_info: { code: 0 } });
    expect(await listInstrumentHolders(input)).toMatchObject({ holders: [], verified: false });

    mocks.callTrade.mockResolvedValue({ ret_info: { code: 0 }, holders: [] });
    expect(await listInstrumentHolders(input)).toMatchObject({ holders: [], verified: true, instrumentUnknown: false });

    mocks.callTrade.mockResolvedValue({ ret_info: { code: 0 }, instrument_unknown: true });
    expect(await listInstrumentHolders(input)).toMatchObject({ holders: [], verified: true, instrumentUnknown: true });
  });

  it("登记下架带 crypto 空间头，created 缺省按已登记过处理", async () => {
    mocks.callControl.mockResolvedValue({ event_id: "delist-1" });
    const req = { exchange: "binance", market: "SPOT", exchange_symbol: "FOOUSDT", delist_at_ms: 1, paper_only: false } as const;
    const result = await registerDelistNotice(req);
    expect(mocks.callControl).toHaveBeenCalledWith("collector", "RegisterDelistNotice", req, {
      headers: { "X-Space-Id": "crypto" }
    });
    expect(result).toEqual({ created: false, event_id: "delist-1" });
  });
});
