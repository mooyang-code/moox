import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// 拿到管理接口客户端注册的响应拦截器：服务端时间必须经由真实的响应路径记录，而不只是直接调用 recordServerDate。
const transport = vi.hoisted(() => ({ responseUse: vi.fn() }));
vi.mock("axios", () => ({
  default: { create: () => ({ post: vi.fn(), interceptors: { response: { use: transport.responseUse } } }) }
}));
vi.mock("./signed-client", () => ({ installSpaceAwareSignedClient: vi.fn(), expireBrowserSession: vi.fn() }));
vi.mock("@arco-design/web-vue", () => ({ Message: { error: vi.fn() } }));

import { recordServerDate, serverNow } from "./http";

describe("server time from the Date header", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-10-09T08:00:00Z"));
  });
  afterEach(() => vi.useRealTimers());

  it("follows the server clock when it is ahead or behind the browser", () => {
    recordServerDate("Fri, 09 Oct 2026 08:01:00 GMT");
    expect(serverNow()).toBe(Date.parse("2026-10-09T08:01:00Z"));
    vi.advanceTimersByTime(10_000);
    expect(serverNow()).toBe(Date.parse("2026-10-09T08:01:10Z"));
    recordServerDate("Fri, 09 Oct 2026 07:50:10 GMT");
    expect(serverNow()).toBe(Date.parse("2026-10-09T07:50:10Z"));
  });

  it("keeps the last offset when the header is missing or unreadable", () => {
    recordServerDate("Fri, 09 Oct 2026 08:05:00 GMT");
    recordServerDate(undefined);
    recordServerDate("not a date");
    expect(serverNow()).toBe(Date.parse("2026-10-09T08:05:00Z"));
  });

  it("records the Date header of every admin response", () => {
    const fulfilled = transport.responseUse.mock.calls[0][0];
    const response = { data: {}, headers: { date: "Fri, 09 Oct 2026 08:20:00 GMT" } };
    expect(fulfilled(response)).toBe(response);
    expect(serverNow()).toBe(Date.parse("2026-10-09T08:20:00Z"));
  });
});
