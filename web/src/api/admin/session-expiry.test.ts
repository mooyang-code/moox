import { afterEach, describe, expect, it, vi } from "vitest";
import { AuthSessionExpiredError } from "./auth-errors";
import { expireBrowserSession, setSessionExpiredListener } from "./session-expiry";

afterEach(() => {
  setSessionExpiredListener(null);
  window.location.hash = "#/";
});

describe("session-expiry", () => {
  it("会话失效时清掉页面里的账号并带着当前地址去登录页", () => {
    const cleared = vi.fn();
    setSessionExpiredListener(cleared);
    window.location.hash = "#/collector/tasks";

    const error = expireBrowserSession();

    expect(error).toBeInstanceOf(AuthSessionExpiredError);
    expect(cleared).toHaveBeenCalledTimes(1);
    expect(window.location.hash).toBe(`#/login?redirect=${encodeURIComponent("/collector/tasks")}`);
  });

  it("已在登录页时不重复套 redirect", () => {
    window.location.hash = "#/login?redirect=%2Fhome";
    expireBrowserSession();
    expect(window.location.hash).toBe("#/login?redirect=%2Fhome");
  });
});
