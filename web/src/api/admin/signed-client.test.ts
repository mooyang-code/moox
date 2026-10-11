import "fake-indexeddb/auto";
import { AxiosHeaders, type InternalAxiosRequestConfig } from "axios";
import { afterEach, describe, expect, it } from "vitest";
import { setSelectedSpaceIdCache } from "./space-header";
import { AuthSessionExpiredError } from "./auth-errors";
import { expireBrowserSession, installSpaceAwareSignedClient, setSpaceInitializer } from "./signed-client";

afterEach(() => {
  localStorage.removeItem("user-info");
  window.location.hash = "#/";
});

describe("installSpaceAwareSignedClient", () => {
  it("registers the Space header interceptor after signing so Axios executes it first", async () => {
    const handlers: Array<
      (config: InternalAxiosRequestConfig) => InternalAxiosRequestConfig | Promise<InternalAxiosRequestConfig>
    > = [];
    const client = {
      interceptors: {
        request: {
          use: (
            handler: (config: InternalAxiosRequestConfig) => InternalAxiosRequestConfig | Promise<InternalAxiosRequestConfig>
          ) => handlers.push(handler)
        },
        response: { use: () => undefined }
      }
    };
    setSelectedSpaceIdCache("space-1");

    installSpaceAwareSignedClient(client as never);

    expect(handlers).toHaveLength(2);
    const config = { headers: new AxiosHeaders() } as InternalAxiosRequestConfig;
    const prepared = await handlers[1](config);
    expect(prepared.headers.get("X-Space-Id")).toBe("space-1");
    setSelectedSpaceIdCache("");
  });

  it("clears the browser session and redirects when authentication expires", async () => {
    localStorage.setItem("user-info", JSON.stringify({ token: "token", sessionId: "session" }));
    window.location.hash = "#/collector/tasks";

    const error = await expireBrowserSession();

    expect(error).toBeInstanceOf(AuthSessionExpiredError);
    expect(localStorage.getItem("user-info")).toBeNull();
    expect(window.location.hash).toBe("#/login");
  });

  function spaceHandler() {
    const handlers: Array<(config: InternalAxiosRequestConfig) => Promise<InternalAxiosRequestConfig>> = [];
    const client = {
      interceptors: { request: { use: (handler: never) => handlers.push(handler) }, response: { use: () => undefined } }
    };
    installSpaceAwareSignedClient(client as never);
    return handlers[1];
  }

  it("waits for the Space initializer before sending a Space-scoped request without a selected Space", async () => {
    setSelectedSpaceIdCache("");
    let calls = 0;
    setSpaceInitializer(async () => {
      calls += 1;
      await Promise.resolve();
      setSelectedSpaceIdCache("crypto");
    });
    const config = { url: "/api/admin/trade/ListOrders", headers: new AxiosHeaders() } as InternalAxiosRequestConfig;
    const prepared = await spaceHandler()(config);
    expect(calls).toBe(1);
    expect(prepared.headers.get("X-Space-Id")).toBe("crypto");
    setSpaceInitializer(null);
    setSelectedSpaceIdCache("");
  });

  it("does not wait for the initializer on login and Space list requests", async () => {
    setSelectedSpaceIdCache("");
    let calls = 0;
    setSpaceInitializer(async () => {
      calls += 1;
    });
    for (const url of ["/api/admin/auth/Login", "/api/admin/space/ListSpaces"]) {
      await spaceHandler()({ url, headers: new AxiosHeaders() } as InternalAxiosRequestConfig);
    }
    expect(calls).toBe(0);
    setSpaceInitializer(null);
  });
});
