import { beforeEach, describe, expect, it, vi } from "vitest";

const { client } = vi.hoisted(() => ({ client: { post: vi.fn(), interceptors: { response: { use: vi.fn() } } } }));
vi.mock("axios", () => ({ default: { create: () => client } }));
vi.mock("@/api/gateway", () => ({ gatewayOrigin: () => "http://localhost" }));
vi.mock("../admin/signed-client", () => ({ installSpaceAwareSignedClient: vi.fn() }));
const { messageError } = vi.hoisted(() => ({ messageError: vi.fn() }));
vi.mock("@arco-design/web-vue", () => ({ Message: { error: messageError } }));

import {
  callTrade,
  isTradeAdoptionRequired,
  isTradeNotFound,
  isTradeSessionNotReady,
  TradeResponseError,
  withDialogErrors
} from "./http";

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

  it("确认对话框提交期间的写操作出错不弹全局提示，结束后恢复", async () => {
    client.post.mockResolvedValue({ data: { ret_info: { code: 1, msg: "暂停失败" } } });
    await expect(withDialogErrors(() => callTrade("console", "PauseLogicalAccount", {}))).rejects.toThrow("暂停失败");
    expect(client.post.mock.calls.at(-1)?.[2]).toEqual({ silentError: true });
    expect(messageError).not.toHaveBeenCalled();
    await expect(callTrade("console", "PauseLogicalAccount", {})).rejects.toThrow();
    expect(messageError).toHaveBeenCalledTimes(1);
  });

  it("传输错误也听 silent：后台轮询的请求带上 silentError，拦截器据此不弹提示", async () => {
    client.post.mockResolvedValue({ data: { ret_info: { code: 0 } } });
    await callTrade("console", "GetLogicalAccount", {}, { silent: true });
    expect(client.post.mock.calls.at(-1)?.[2]).toEqual({ silentError: true });
    const onError = client.interceptors.response.use.mock.calls[0][1] as (error: unknown) => Promise<unknown>;
    // 静默时不弹提示，但网络错误照样改写成中文：下单等对话框会显示 error.message
    const silentFailure = { config: { silentError: true }, message: "Network Error" };
    await expect(onError(silentFailure)).rejects.toBeTruthy();
    expect(messageError).not.toHaveBeenCalled();
    expect(silentFailure.message).not.toBe("Network Error");
    await expect(onError({ config: {}, message: "Network Error" })).rejects.toBeTruthy();
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
    client.post.mockResolvedValue({ data: { ret_info: { code: 1, msg: "trade: invalid trading account" } } });
    const error = await callTrade("console", "UpdateTradingAccount", {}, { silent: true }).catch(cause => cause);
    expect(error.message).toBe("执行账户的参数不合法");
    expect(error.response.ret_info.msg, "原始消息保留").toBe("trade: invalid trading account");
    expect(isTradeNotFound(error)).toBe(false);
    expect(isTradeNotFound(new TradeResponseError({ ret_info: { code: "NOT_FOUND", msg: "" } }))).toBe(true);
    expect(new TradeResponseError({ ret_info: { code: 1, msg: "执行账户上已有持仓或挂单" } }).message).toBe(
      "执行账户上已有持仓或挂单"
    );
  });

  it("会话未启动、需要接管按专门的错误码识别，数字和枚举名都认", () => {
    const error = (code: number | string) => new TradeResponseError({ ret_info: { code, msg: "" } });
    expect(isTradeSessionNotReady(error(19))).toBe(true);
    expect(isTradeSessionNotReady(error("TRADE_SESSION_NOT_READY"))).toBe(true);
    expect(isTradeSessionNotReady(error(4))).toBe(false);
    expect(isTradeAdoptionRequired(error(20))).toBe(true);
    expect(isTradeAdoptionRequired(error("TRADE_ADOPTION_REQUIRED"))).toBe(true);
    expect(isTradeAdoptionRequired(error(1))).toBe(false);
    expect(isTradeSessionNotReady(new Error("交易账户会话还没有启动"))).toBe(false);
  });

  it("包装进去的具体原因保留在括号里；冲突类不接原文；只认完整的错误常量", () => {
    const text = (msg: string) => new TradeResponseError({ ret_info: { code: 14, msg } }).message;
    expect(text("trade store: conflict: stale paper order")).toBe("数据已经被修改，刷新后再试");
    expect(text("trade store: conflict")).toBe("数据已经被修改，刷新后再试");
    expect(text("trade: invalid order transition")).toBe("trade: invalid order transition");
    expect(text("trade: invalid order: quantity must be positive")).toBe("订单参数不合法（quantity must be positive）");
    expect(text("trade: invalid order specification")).toBe("订单参数不合法");
  });

  it("“记录不合法”只是外层包装：剥掉英文前缀只显示后面的中文原因", () => {
    const text = (msg: string) => new TradeResponseError({ ret_info: { code: 1, msg } }).message;
    expect(text("trade store: invalid record: 修改组合成员前要先暂停组合")).toBe("修改组合成员前要先暂停组合");
    expect(text("trade store: invalid record: 执行账户已属于其他组合账户，先从该组合移除")).toBe(
      "执行账户已属于其他组合账户，先从该组合移除"
    );
    expect(text("trade store: invalid record")).toBe("数据不合法");
  });

  it("“记录不合法”后面接的是英文原因时照常显示“数据不合法（原因）”；全角冒号同样认得", () => {
    const text = (msg: string) => new TradeResponseError({ ret_info: { code: 1, msg } }).message;
    expect(text("trade store: invalid record: missing logical account")).toBe("数据不合法（missing logical account）");
    expect(text("trade store: invalid record：修改组合成员前要先暂停组合")).toBe("修改组合成员前要先暂停组合");
    expect(text("trade store: invalid record：bad state")).toBe("数据不合法（bad state）");
    expect(text("trade: invalid order：数量必须大于 0")).toBe("订单参数不合法（数量必须大于 0）");
    expect(text("trade store: conflict：")).toBe("数据已经被修改，刷新后再试");
    expect(text("trade: invalid order transition")).toBe("trade: invalid order transition");
  });

  it("名称重复、唯一约束冲突的固定文案原样显示，不被冲突类的映射替换", () => {
    const text = (code: string, msg: string) => new TradeResponseError({ ret_info: { code, msg } }).message;
    const taken = "名称已被使用，请换一个（已关闭的模拟账户也占用名称）";
    expect(text("INVALID_PARAM", taken)).toBe(taken);
    const unique = "保存的数据和已有记录冲突（重复提交或刚被别处修改），刷新后再试";
    expect(text("CONFLICT", unique)).toBe(unique);
    expect(text("CONFLICT", "trade store: conflict: stale order version")).toBe("数据已经被修改，刷新后再试");
  });

  it("does not invent acceptance when the response envelope is missing", async () => {
    client.post.mockResolvedValue({ data: {} });
    await expect(callTrade("console", "SubmitOrder", {})).rejects.toThrow("缺少 ret_info");
  });
});
