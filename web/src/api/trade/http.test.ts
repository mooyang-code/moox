import { beforeEach, describe, expect, it, vi } from "vitest";

const { client } = vi.hoisted(() => ({ client: { post: vi.fn(), interceptors: { response: { use: vi.fn() } } } }));
vi.mock("axios", () => ({ default: { create: () => client } }));
vi.mock("@/api/gateway", () => ({ gatewayOrigin: () => "http://localhost" }));
vi.mock("../admin/signed-client", () => ({ installSpaceAwareSignedClient: vi.fn() }));
const { messageError } = vi.hoisted(() => ({ messageError: vi.fn() }));
vi.mock("@arco-design/web-vue", () => ({ Message: { error: messageError } }));

import { callTrade, isTradeInvalidParam, isTradeNotFound, TradeResponseError } from "./http";

describe("Trade response errors", () => {
  beforeEach(() => {
    client.post.mockReset();
    messageError.mockReset();
  });

  it("写操作失败时提示业务错误，查询和指定静默的调用不提示", async () => {
    client.post.mockResolvedValue({ data: { ret_info: { code: 1, msg: "暂停失败" } } });
    await expect(callTrade("console", "ResumeLogicalAccount", {})).rejects.toThrow("暂停失败");
    expect(messageError).toHaveBeenCalledTimes(1);
    await expect(callTrade("console", "ListOrders", {})).rejects.toThrow();
    await expect(callTrade("console", "GetOperatorAction", {})).rejects.toThrow();
    await expect(callTrade("console", "SubmitOrder", {}, { silent: true })).rejects.toThrow();
    expect(messageError).toHaveBeenCalledTimes(1);
  });

  it("retains the durable action when admission returns a retryable error", async () => {
    const response = {
      ret_info: { code: 1, msg: "market data unavailable" },
      action: { action_id: "same-action", status: "RUNNING" }
    };
    client.post.mockResolvedValue({ data: response });
    await expect(callTrade("console", "SubmitOrder", { action_id: "same-action" })).rejects.toMatchObject({
      message: "market data unavailable",
      response
    });
  });

  it("returns successful pending acceptance without converting it to a fill", async () => {
    const response = { ret_info: { code: 0, msg: "" }, action: { status: "RUNNING" }, order: { state: "PENDING" } };
    client.post.mockResolvedValue({ data: response });
    await expect(callTrade("console", "SubmitOrder", {})).resolves.toEqual(response);
  });

  it("没有消息时给出中文兜底和错误码；已知的英文错误换成中文，未知的原样返回", async () => {
    client.post.mockResolvedValue({ data: { ret_info: { code: 13, msg: "" } } });
    await expect(callTrade("console", "ListOrders", {})).rejects.toThrow("交易服务请求失败（错误码 13）");
    client.post.mockResolvedValue({ data: { ret_info: { code: 1, msg: "trade logical account: adoption is required" } } });
    const error = await callTrade("console", "AddLogicalAccountMember", {}, { silent: true }).catch(cause => cause);
    expect(error.message).toContain("接管账户里已有的持仓");
    expect(error.response.ret_info.msg, "原始消息保留").toBe("trade logical account: adoption is required");
    expect(isTradeInvalidParam(error)).toBe(true);
    expect(isTradeNotFound(error)).toBe(false);
    expect(isTradeNotFound(new TradeResponseError({ ret_info: { code: "NOT_FOUND", msg: "" } }))).toBe(true);
  });

  it("does not invent acceptance when the response envelope is missing", async () => {
    client.post.mockResolvedValue({ data: {} });
    await expect(callTrade("console", "SubmitOrder", {})).rejects.toThrow("缺少 ret_info");
  });
});
