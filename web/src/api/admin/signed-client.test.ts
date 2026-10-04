import "fake-indexeddb/auto";
import { AxiosHeaders, type InternalAxiosRequestConfig } from "axios";
import { afterEach, describe, expect, it } from "vitest";
import { setSelectedSpaceIdCache } from "./space-header";
import { AuthSessionExpiredError } from "./auth-errors";
import { expireBrowserSession, installSpaceAwareSignedClient } from "./signed-client";

afterEach(() => {
  localStorage.removeItem("user-info");
  window.location.hash = "#/";
});

describe("installSpaceAwareSignedClient", () => {
  it("registers the Space header interceptor after signing so Axios executes it first", () => {
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
    const prepared = handlers[1](config) as InternalAxiosRequestConfig;
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
});
