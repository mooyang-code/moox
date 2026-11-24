import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AuthSessionExpiredError } from "./admin/auth-errors";
import { StaleSpaceRequestError } from "./admin/stale-space";
import { NETWORK_ERROR_MESSAGE } from "./network-error";

const transport = vi.hoisted(() => ({ post: vi.fn(), responseUse: vi.fn() }));
vi.mock("axios", () => ({
  default: { create: () => ({ post: transport.post, interceptors: { response: { use: transport.responseUse } } }) }
}));
vi.mock("./admin/space-client", async importOriginal => ({
  ...(await importOriginal<typeof import("./admin/space-client")>()),
  installSpaceAwareClient: vi.fn()
}));
const { messageError } = vi.hoisted(() => ({ messageError: vi.fn() }));
vi.mock("@arco-design/web-vue", () => ({ Message: { error: messageError } }));

import { call, recordServerDate, reportError, RequestError, serverNow } from "./http";

beforeEach(() => {
  transport.post.mockReset().mockResolvedValue({ data: { ret_info: { code: 0 } } });
  messageError.mockReset();
});

describe("call：请求层不弹提示，失败抛给页面", () => {
  it("发到 /api/admin/<服务>/<方法>，成功时返回整个响应体", async () => {
    await expect(call("collector", "GetTaskDetail", {})).resolves.toEqual({ ret_info: { code: 0 } });
    expect(transport.post).toHaveBeenCalledWith("/api/admin/collector/GetTaskDetail", {}, {});
  });

  it("业务失败抛 RequestError，带着完整的响应体和错误码，不弹提示", async () => {
    const response = { ret_info: { code: 1, msg: "state unavailable" }, instance: { instance_id: "instance-1" } };
    transport.post.mockResolvedValue({ data: response });
    const error = await call("strategy", "CreateStrategyInstance", {}).catch(cause => cause);
    expect(error).toBeInstanceOf(RequestError);
    expect(error.message).toBe("state unavailable");
    expect(error.response).toEqual(response);
    expect(error.code).toBe(1);
    expect(error.hasCode("1")).toBe(true);
    expect(messageError).not.toHaveBeenCalled();
  });

  it("HTTP 失败时保留随错误返回的响应体；401 是会话失效，403 是普通失败", async () => {
    const response = { ret_info: { code: 1, msg: "enable failed" }, instance: { instance_id: "instance-1" } };
    transport.post.mockRejectedValueOnce({ response: { status: 500, data: response } });
    await expect(call("strategy", "EnableStrategyInstance", {})).rejects.toMatchObject({ message: "enable failed", response });
    transport.post.mockRejectedValueOnce({ response: { status: 401, data: { ret_info: { code: 4, msg: "登录已失效" } } } });
    await expect(call("strategy", "ListStrategies", {})).rejects.toBeInstanceOf(AuthSessionExpiredError);
    transport.post.mockRejectedValueOnce({ response: { status: 403, data: { ret_info: { code: 4, msg: "来源不对" } } } });
    const forbidden = await call("strategy", "ListStrategies", {}).catch(cause => cause);
    expect(forbidden).toBeInstanceOf(RequestError);
    expect(forbidden.message).toBe("来源不对");
    expect(messageError).not.toHaveBeenCalled();
  });

  it("网络错误换成中文提示；切换空间造成的取消保持原类型", async () => {
    transport.post.mockRejectedValueOnce({ code: "ERR_NETWORK", message: "Network Error" });
    const network = await call("trade", "SubmitOrder", {}).catch(cause => cause);
    expect(network).toBeInstanceOf(RequestError);
    expect(network.message).toBe(NETWORK_ERROR_MESSAGE);
    expect(network.response).toBeUndefined();
    transport.post.mockRejectedValueOnce(new StaleSpaceRequestError());
    await expect(call("trade", "ListOrders", {})).rejects.toBeInstanceOf(StaleSpaceRequestError);
  });

  it("业务失败的提示就是 ret_info.msg，没有消息时写错误码；缺 ret_info 时报错", async () => {
    transport.post.mockResolvedValue({ data: { ret_info: { code: 13, msg: "" } } });
    await expect(call("trade", "ListOrders", {})).rejects.toThrow("trade/ListOrders 请求失败（错误码 13）");
    transport.post.mockResolvedValue({ data: {} });
    await expect(call("trade", "SubmitOrder", {})).rejects.toThrow("缺少 ret_info");
  });

  it("显式空间、超时、登录探测写进请求配置", async () => {
    await call("storage", "ListDatasets", { space_id: "other" }, { spaceId: "other", timeout: 5000 });
    expect(transport.post).toHaveBeenLastCalledWith(
      "/api/admin/storage/ListDatasets",
      { space_id: "other" },
      { headers: { "X-Space-Id": "other" }, timeout: 5000 }
    );
    await call("auth", "GetUserInfo", {}, { authProbe: true });
    expect(transport.post).toHaveBeenLastCalledWith("/api/admin/auth/GetUserInfo", {}, { authProbe: true });
  });
});

describe("Storage 读缓存", () => {
  it("短时读取合并，写入后作废", async () => {
    const req = { space_id: "cache-space" };
    await call("storage", "ListDataSources", req);
    await call("storage", "ListDataSources", req);
    expect(transport.post).toHaveBeenCalledTimes(1);
    await call("storage", "UpdateDataSource", { space_id: "cache-space" });
    await call("storage", "ListDataSources", req);
    expect(transport.post).toHaveBeenCalledTimes(3);
  });

  it("别的服务不缓存", async () => {
    await call("collector", "ListDataSources", {});
    await call("collector", "ListDataSources", {});
    expect(transport.post).toHaveBeenCalledTimes(2);
  });
});

describe("reportError", () => {
  it("同一个错误只提示一次；切换空间的取消不提示", () => {
    const error = new Error("节点下仍有服务实例");
    reportError(error);
    reportError(error);
    reportError(new StaleSpaceRequestError());
    expect(messageError).toHaveBeenCalledOnce();
    expect(messageError).toHaveBeenCalledWith("节点下仍有服务实例");
  });
});

describe("服务端时间", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-10-09T08:00:00Z"));
  });
  afterEach(() => vi.useRealTimers());

  it("跟随响应的 Date 头，读不出时保持原值", () => {
    recordServerDate("Fri, 09 Oct 2026 08:01:00 GMT");
    vi.advanceTimersByTime(10_000);
    expect(serverNow()).toBe(Date.parse("2026-10-09T08:01:10Z"));
    recordServerDate("not a date");
    expect(serverNow()).toBe(Date.parse("2026-10-09T08:01:10Z"));
  });

  it("每个响应都记录 Date 头；trpc-ret 非 0 时按失败处理", async () => {
    const fulfilled = transport.responseUse.mock.calls[0][0];
    const response = { data: {}, headers: { date: "Fri, 09 Oct 2026 08:20:00 GMT" } };
    expect(fulfilled(response)).toBe(response);
    expect(serverNow()).toBe(Date.parse("2026-10-09T08:20:00Z"));
    await expect(fulfilled({ data: {}, headers: { "trpc-ret": "21", "trpc-func-ret": "超时" } })).rejects.toThrow("超时");
  });
});

describe("服务超时", () => {
  it("网关或服务内部超时的英文原文统一显示中文", async () => {
    transport.post.mockResolvedValueOnce({ data: { ret_info: { code: 2, msg: "read tcp 10.1.2.3:1->4.5.6.7:2: i/o timeout" } } });
    await expect(call("trade", "SubmitOrder", {})).rejects.toThrow("服务响应超时，请稍后重试");
    transport.post.mockRejectedValueOnce({
      response: { status: 504, data: { ret_info: { code: 2, msg: "context deadline exceeded" } } }
    });
    await expect(call("strategy", "ListStrategies", {})).rejects.toThrow("服务响应超时，请稍后重试");
  });
});
