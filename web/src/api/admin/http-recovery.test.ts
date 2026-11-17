import { beforeEach, expect, it, vi } from "vitest";
import { AuthSessionExpiredError } from "./auth-errors";

const transport = vi.hoisted(() => ({ post: vi.fn(), responseUse: vi.fn() }));
vi.mock("axios", () => ({
  default: { create: () => ({ post: transport.post, interceptors: { response: { use: transport.responseUse } } }) }
}));
vi.mock("./space-client", () => ({ installSpaceAwareClient: vi.fn() }));
vi.mock("@arco-design/web-vue", () => ({ Message: { error: vi.fn() } }));

import { callControl } from "./http";

beforeEach(() => {
  transport.post.mockReset();
});

it("preserves durable instance identity on a business failure", async () => {
  const response = {
    ret_info: { code: 1, msg: "state unavailable" },
    instance: { instance_id: "instance-1", space_id: "space-1", strategy_id: "strategy-1" }
  };
  transport.post.mockResolvedValue({ data: response });
  const error = await callControl("strategy", "CreateStrategyInstance", {}).catch(error => error);
  expect(error).toBeInstanceOf(Error);
  expect(error.message).toBe("state unavailable");
  expect(error.response).toEqual(response);
  expect(error.response.instance).not.toHaveProperty("session_id");
});

it("preserves a recovery response returned with a non-2xx status", async () => {
  const response = { ret_info: { code: 1, msg: "enable failed" }, instance: { instance_id: "instance-1" } };
  const reject = transport.responseUse.mock.calls[0][1];
  await expect(reject({ response: { data: response } })).rejects.toMatchObject({ message: "enable failed", response });
});

// 服务端返回 401（会话失效）时交给调用方 AuthSessionExpiredError；403（来源不对）仍是普通的请求错误。
it("maps a 401 to an authentication expiry and keeps 403 an ordinary failure", async () => {
  const reject = transport.responseUse.mock.calls[0][1];
  await expect(
    reject({ response: { status: 401, data: { ret_info: { code: 4, msg: "登录已失效，请重新登录" } } }, config: {} })
  ).rejects.toBeInstanceOf(AuthSessionExpiredError);
  const forbidden = await reject({
    response: { status: 403, data: { ret_info: { code: 4, msg: "来源不对" } } },
    config: {}
  }).catch((error: unknown) => error);
  expect(forbidden).not.toBeInstanceOf(AuthSessionExpiredError);
  expect(forbidden).toMatchObject({ message: "来源不对" });
});

// 会话在 HttpOnly Cookie 里，页面读不到：请求照常发出，由服务端判断是否登录。
it("sends requests without checking any locally stored token", async () => {
  transport.post.mockResolvedValue({ data: { ret_info: { code: 0 } } });
  await expect(callControl("collector", "GetTaskDetail", {})).resolves.toEqual({ ret_info: { code: 0 } });
  expect(transport.post).toHaveBeenCalledWith("/api/admin/collector/GetTaskDetail", {}, undefined);
});
