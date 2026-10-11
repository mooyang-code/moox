import "fake-indexeddb/auto";
import { AxiosHeaders, type InternalAxiosRequestConfig } from "axios";
import { afterEach, describe, expect, it } from "vitest";
import { selectedSpaceGeneration, setSelectedSpaceIdCache } from "./space-header";
import { StaleSpaceRequestError } from "./stale-space";
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

  it("makes concurrent requests wait for the same in-flight Space initialization", async () => {
    setSelectedSpaceIdCache("");
    let calls = 0;
    let finish!: () => void;
    setSpaceInitializer(
      () =>
        new Promise<void>(resolve => {
          calls += 1;
          finish = () => {
            setSelectedSpaceIdCache("crypto");
            resolve();
          };
        })
    );
    const handler = spaceHandler();
    const config = () => ({ url: "/api/admin/trade/ListOrders", headers: new AxiosHeaders() }) as InternalAxiosRequestConfig;
    const first = handler(config());
    const second = handler(config()); // 冷却期内的并发请求也必须等待，而不是不带空间头直接发出
    await Promise.resolve();
    finish();
    const [a, b] = await Promise.all([first, second]);
    expect(calls).toBe(1);
    expect(a.headers.get("X-Space-Id")).toBe("crypto");
    expect(b.headers.get("X-Space-Id")).toBe("crypto");
    setSpaceInitializer(null);
    setSelectedSpaceIdCache("");
  });

  it("rejects stale space reads (late response or aborted) with StaleSpaceRequestError, keeps writes, global services and explicit Space requests", async () => {
    type ResponseOk = (response: { config: Record<string, unknown>; data: string }) => unknown;
    type ResponseErr = (error: { config: Record<string, unknown> }) => unknown;
    let onOk: ResponseOk = () => undefined;
    let onErr: ResponseErr = () => undefined;
    let captured = false;
    const client = {
      interceptors: {
        request: { use: () => undefined },
        response: {
          use: (ok?: ResponseOk, err?: ResponseErr) => {
            // 只记录第一个登记的响应拦截器（空间过期处理），后面登记的是签名层自己的
            if (captured) return;
            captured = true;
            if (ok) onOk = ok;
            if (err) onErr = err;
          }
        }
      }
    };
    installSpaceAwareSignedClient(client as never);
    setSelectedSpaceIdCache("space-b");
    const gen = selectedSpaceGeneration();

    const staleRead = {
      config: { url: "/api/admin/collector/GetTaskInstanceList", injectedSpaceId: "space-a", injectedSpaceGeneration: gen - 1 },
      data: "A"
    };
    await expect(Promise.resolve().then(() => onOk(staleRead))).rejects.toBeInstanceOf(StaleSpaceRequestError);
    await expect(Promise.resolve().then(() => onErr({ config: staleRead.config }))).rejects.toBeInstanceOf(
      StaleSpaceRequestError
    );
    // A→B→A：空间 ID 相同但代次已变
    const roundTrip = {
      config: { url: "/api/admin/collector/GetTask", injectedSpaceId: "space-b", injectedSpaceGeneration: gen - 1 },
      data: "R"
    };
    await expect(Promise.resolve().then(() => onOk(roundTrip))).rejects.toBeInstanceOf(StaleSpaceRequestError);

    const sameSpace = {
      config: { url: "/api/admin/collector/GetTaskInstanceList", injectedSpaceId: "space-b", injectedSpaceGeneration: gen },
      data: "B"
    };
    expect(onOk(sameSpace)).toBe(sameSpace);
    const staleWrite = {
      config: { url: "/api/admin/collector/CreateTask", injectedSpaceId: "space-a", injectedSpaceGeneration: gen - 1 },
      data: "W"
    };
    expect(onOk(staleWrite)).toBe(staleWrite);
    const globalRead = {
      config: { url: "/api/admin/sysdeploy/GetCatalog", injectedSpaceId: "space-a", injectedSpaceGeneration: gen - 1 },
      data: "G"
    };
    expect(onOk(globalRead)).toBe(globalRead);
    const explicit = { config: { url: "/api/admin/collector/GetTask" }, data: "E" };
    expect(onOk(explicit)).toBe(explicit);
    setSelectedSpaceIdCache("");
  });

  it("aborts in-flight space reads the moment the Space changes, so finally blocks run", async () => {
    setSelectedSpaceIdCache("space-a");
    const handlers: Array<
      (config: InternalAxiosRequestConfig) => InternalAxiosRequestConfig | Promise<InternalAxiosRequestConfig>
    > = [];
    const client = {
      interceptors: {
        request: { use: (handler: (typeof handlers)[number]) => handlers.push(handler) },
        response: { use: () => undefined }
      }
    };
    installSpaceAwareSignedClient(client as never);
    const spaceHandler = handlers[handlers.length - 1];
    const read = await spaceHandler({
      url: "/api/admin/collector/GetTask",
      headers: new AxiosHeaders()
    } as InternalAxiosRequestConfig);
    const global = await spaceHandler({
      url: "/api/admin/sysdeploy/GetCatalog",
      headers: new AxiosHeaders()
    } as InternalAxiosRequestConfig);
    const write = await spaceHandler({
      url: "/api/admin/collector/CreateTask",
      headers: new AxiosHeaders()
    } as InternalAxiosRequestConfig);
    expect(read.signal?.aborted).toBe(false);
    setSelectedSpaceIdCache("space-b");
    expect(read.signal?.aborted).toBe(true);
    expect(global.signal).toBeUndefined();
    expect(write.signal).toBeUndefined();
    setSelectedSpaceIdCache("");
  });
});
