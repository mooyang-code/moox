import { describe, expect, it } from "vitest";
import { gatewayOrigin, gatewayURL, gatewayWebSocketURL } from "./gateway";

describe("production gateway URLs", () => {
  it("uses the browser origin without a direct Admin port", () => {
    expect(gatewayOrigin()).toBe(window.location.origin);
    expect(gatewayURL("/api/admin/auth/Login")).toBe(`${window.location.origin}/api/admin/auth/Login`);
    expect(gatewayWebSocketURL("/api/admin/ssh/WsConnect?ticket=t")).toBe(
      `${window.location.origin.replace(/^http/, "ws")}/api/admin/ssh/WsConnect?ticket=t`
    );
  });
});
