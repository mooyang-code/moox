import { beforeEach, describe, expect, it, vi } from "vitest";
import { Message } from "@arco-design/web-vue";
import { isNetworkFailure, NETWORK_ERROR_MESSAGE, reportControlError } from "./http";

vi.mock("@arco-design/web-vue", () => ({ Message: { error: vi.fn() } }));

describe("control error feedback", () => {
  beforeEach(() => vi.mocked(Message.error).mockClear());

  it("shows business errors that were not reported by the transport interceptor", () => {
    reportControlError(new Error("节点下仍有服务实例"));
    expect(Message.error).toHaveBeenCalledWith("节点下仍有服务实例");
  });

  it("识别网络层失败：没有收到响应才算，服务端返回的错误不算", () => {
    expect(isNetworkFailure({ code: "ERR_NETWORK", message: "Network Error" })).toBe(true);
    expect(isNetworkFailure({ message: "Network Error" })).toBe(true);
    expect(isNetworkFailure({ code: "ERR_NETWORK", response: { status: 502 } })).toBe(false);
    expect(isNetworkFailure({ code: "ECONNABORTED", message: "timeout of 30000ms exceeded" })).toBe(false);
    expect(isNetworkFailure(null)).toBe(false);
    expect(NETWORK_ERROR_MESSAGE).toContain("根证书");
    expect(NETWORK_ERROR_MESSAGE).not.toContain("Network Error");
  });

  it("does not show the same error twice", () => {
    const error = new Error("network unavailable");
    reportControlError(error);
    reportControlError(error);
    expect(Message.error).toHaveBeenCalledOnce();
  });
});
