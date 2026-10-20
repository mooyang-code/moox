import { describe, expect, it } from "vitest";
import { explainTransportError, isNetworkFailure, NETWORK_ERROR_MESSAGE } from "./network-error";

describe("网络层失败的提示", () => {
  it("识别没有收到响应的失败：服务端返回的错误和超时不算", () => {
    expect(isNetworkFailure({ code: "ERR_NETWORK", message: "Network Error" })).toBe(true);
    expect(isNetworkFailure({ message: "Network Error" })).toBe(true);
    expect(isNetworkFailure({ code: "ERR_NETWORK", response: { status: 502 } })).toBe(false);
    expect(isNetworkFailure({ code: "ECONNABORTED", message: "timeout of 30000ms exceeded" })).toBe(false);
    expect(isNetworkFailure(null)).toBe(false);
  });

  it("文案是可操作的中文提示，指向根证书并提醒彻底重启浏览器", () => {
    expect(NETWORK_ERROR_MESSAGE).toContain("根证书");
    expect(NETWORK_ERROR_MESSAGE).toContain("彻底重启浏览器");
    expect(NETWORK_ERROR_MESSAGE).not.toContain("Network Error");
  });

  it("网络层失败：返回友好文案，并把原错误对象的 message 一并改写，调用方展示的是同一句话", () => {
    const error = Object.assign(new Error("Network Error"), { code: "ERR_NETWORK" });
    expect(explainTransportError(error, "Storage 请求失败")).toBe(NETWORK_ERROR_MESSAGE);
    expect(error.message).toBe(NETWORK_ERROR_MESSAGE);
  });

  it("其它错误保持原文案，没有文案时用兜底", () => {
    expect(explainTransportError(new Error("节点下仍有服务实例"), "兜底")).toBe("节点下仍有服务实例");
    expect(explainTransportError({ response: { status: 500 } }, "Trade 请求失败")).toBe("Trade 请求失败");
    expect(explainTransportError(null, "兜底")).toBe("兜底");
  });
});
