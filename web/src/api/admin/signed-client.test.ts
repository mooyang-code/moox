import { AxiosHeaders, type InternalAxiosRequestConfig } from "axios";
import { describe, expect, it } from "vitest";
import { setSelectedSpaceIdCache } from "./space-header";
import { installSpaceAwareSignedClient } from "./signed-client";

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
});
