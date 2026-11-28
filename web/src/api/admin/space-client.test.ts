import { AxiosHeaders, type InternalAxiosRequestConfig } from "axios";
import { afterEach, describe, expect, it, vi } from "vitest";
import { selectedSpaceGeneration, setSelectedSpaceIdCache } from "./space-header";
import { StaleSpaceRequestError } from "./stale-space";
import { AuthSessionExpiredError } from "./auth-errors";
import {
  expireBrowserSession,
  installSpaceAwareClient,
  setSessionExpiredListener,
  setSpaceInitializer,
  spacePolicyForUrl
} from "./space-client";

afterEach(() => {
  setSessionExpiredListener(null);
  window.location.hash = "#/";
});

describe("installSpaceAwareClient", () => {
  it("registers only the Space header interceptor: no signing headers, the browser carries the session cookie", async () => {
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

    installSpaceAwareClient(client as never);

    expect(handlers).toHaveLength(1);
    const config = { headers: new AxiosHeaders(), url: "/api/admin/x/Get" } as InternalAxiosRequestConfig;
    const prepared = await handlers[0](config);
    expect(prepared.headers.get("X-Space-Id")).toBe("space-1");
    for (const name of ["Authorization", "X-Access-Token", "X-Moox-Signature", "X-Moox-Nonce", "X-Moox-Timestamp"]) {
      expect(prepared.headers.get(name)).toBeUndefined();
    }
    setSelectedSpaceIdCache("");
  });

  it("clears the account and redirects with the current page when the session expires", () => {
    const cleared = vi.fn();
    setSessionExpiredListener(cleared);
    window.location.hash = "#/collector/tasks";

    const error = expireBrowserSession();

    expect(error).toBeInstanceOf(AuthSessionExpiredError);
    expect(cleared).toHaveBeenCalledTimes(1);
    // 带着原地址去登录页，登录后回到采集任务页
    expect(window.location.hash).toBe(`#/login?redirect=${encodeURIComponent("/collector/tasks")}`);
  });

  it("does not wrap the login page in another redirect", () => {
    window.location.hash = "#/login?redirect=%2Fhome";
    expireBrowserSession();
    expect(window.location.hash).toBe("#/login?redirect=%2Fhome");
  });

  function responseErrorHandler() {
    const handlers: Array<(error: unknown) => Promise<unknown>> = [];
    const client = {
      interceptors: {
        request: { use: () => undefined },
        response: { use: (_ok: unknown, onError?: (error: unknown) => Promise<unknown>) => onError && handlers.push(onError) }
      }
    };
    installSpaceAwareClient(client as never);
    return handlers[handlers.length - 1];
  }

  it("expires the session only on 401, and never for login probes or other statuses", async () => {
    const cleared = vi.fn();
    setSessionExpiredListener(cleared);
    window.location.hash = "#/collector/tasks";
    const onError = responseErrorHandler();

    for (const error of [
      { response: { status: 403 }, config: {} },
      { response: { status: 429 }, config: {} },
      { response: { status: 401 }, config: { authProbe: true } }
    ]) {
      await expect(onError(error)).rejects.toBe(error);
    }
    expect(cleared).not.toHaveBeenCalled();
    expect(window.location.hash).toBe("#/collector/tasks");

    const expired = { response: { status: 401 }, config: {} };
    await expect(onError(expired)).rejects.toBe(expired);
    expect(cleared).toHaveBeenCalledTimes(1);
    expect(window.location.hash).toBe(`#/login?redirect=${encodeURIComponent("/collector/tasks")}`);
  });

  function spaceHandler() {
    const handlers: Array<(config: InternalAxiosRequestConfig) => Promise<InternalAxiosRequestConfig>> = [];
    const client = {
      interceptors: { request: { use: (handler: never) => handlers.push(handler) }, response: { use: () => undefined } }
    };
    installSpaceAwareClient(client as never);
    return handlers[0];
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
    for (const url of ["/api/admin/auth/Login", "/api/admin/storage/ListSpaces", "/api/admin/storage/CreateSpace"]) {
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
            // 只记录第一个登记的响应拦截器（空间过期处理），后面登记的是会话失效处理
            if (captured) return;
            captured = true;
            if (ok) onOk = ok;
            if (err) onErr = err;
          }
        }
      }
    };
    installSpaceAwareClient(client as never);
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

  it("rejects in-flight space reads the moment the Space changes, independent of request progress", async () => {
    setSelectedSpaceIdCache("space-a");
    const seen: Array<{ url: string; signal?: AbortSignal }> = [];
    const hang = (url: string, _data?: unknown, config?: { signal?: AbortSignal }) => {
      seen.push({ url, signal: config?.signal });
      return new Promise(() => undefined);
    };
    const client = {
      interceptors: { request: { use: () => undefined }, response: { use: () => undefined } },
      post: hang,
      get: hang,
      request: (config: { url?: string; signal?: AbortSignal }) => hang(config.url || "", undefined, config)
    };
    installSpaceAwareClient(client as never);
    const read = client.post("/api/admin/collector/GetTask", {});
    void client.post("/api/admin/sysdeploy/GetCatalog", {});
    void client.post("/api/admin/collector/ListCloudAccounts", {});
    void client.post("/api/admin/collector/CreateTask", {});
    const outcome = read.then(
      () => "resolved",
      error => (error instanceof StaleSpaceRequestError ? "stale" : "other")
    );
    setSelectedSpaceIdCache("space-b");
    expect(await outcome).toBe("stale");
    expect(seen[0].signal?.aborted).toBe(true);
    for (const exempt of seen.slice(1)) expect(exempt.signal).toBeUndefined(); // 全局读取与写入不被取消
    setSelectedSpaceIdCache("");
  });

  it("registers reads issued before the Space initialization finishes, so a later switch still cancels them", async () => {
    setSelectedSpaceIdCache("");
    let finishInit: () => void = () => undefined;
    setSpaceInitializer(
      () =>
        new Promise<void>(resolve => {
          finishInit = () => {
            setSelectedSpaceIdCache("space-a");
            resolve();
          };
        })
    );
    const seen: Array<{ signal?: AbortSignal }> = [];
    const client = {
      interceptors: { request: { use: () => undefined }, response: { use: () => undefined } },
      post: (_url: string, _data?: unknown, config?: { signal?: AbortSignal }) => {
        seen.push({ signal: config?.signal });
        return new Promise(() => undefined);
      }
    };
    installSpaceAwareClient(client as never);
    const read = client.post("/api/admin/collector/GetTask", {});
    const outcome = read.then(
      () => "resolved",
      error => (error instanceof StaleSpaceRequestError ? "stale" : "other")
    );
    finishInit();
    await new Promise(resolve => setTimeout(resolve, 0)); // 等初始化链走完，读取在 space-a 下登记
    expect(seen).toHaveLength(1);
    setSelectedSpaceIdCache("space-b");
    expect(await outcome).toBe("stale");
    expect(seen[0].signal?.aborted).toBe(true);
    setSpaceInitializer(null);
    setSelectedSpaceIdCache("");
  });

  it("classifies global, space_optional, and space_required endpoints according to D7", () => {
    for (const url of [
      "/api/admin/auth/Login",
      "/api/admin/ssh/CreateSession",
      "/api/admin/secret/ListSecrets",
      "/api/admin/sysdeploy/GetCatalog",
      "/api/admin/monitor/GetHealthOverview",
      "/api/admin/eventbus/GetStatus",
      "/api/admin/storage/ListSpaces",
      "/api/admin/storage/CreateSpace",
      "/api/admin/storage/UpdateSpace",
      "/api/admin/storage/DeleteSpace",
      "/api/admin/collector/ListCloudAccounts",
      "/api/admin/collector/CreateCloudAccount",
      "/api/admin/collector/UpdateCloudAccount",
      "/api/admin/collector/ListCloudRegions",
      "/api/admin/collector/DeleteCloudAccount",
      "/api/admin/collector/GetDataTypeConfigs",
      "/api/admin/collector/GetDataTypeConfigWithFields",
      "/api/admin/collector/ListSubjectListers",
      "/api/admin/collector/RegisterDelistNotice",
      "/api/admin/collector/GetLifecycle",
      "/api/admin/collector/ControlLifecycle",
      "/api/admin/trade/InspectExchangeCredential",
      "/api/admin/trade/ListBlockedInstruments",
      "/api/admin/trade/UnblockInstrument",
      "/api/admin/trade/ListInstrumentHolders",
      "/api/admin/trade/ListDelists",
      "/api/admin/trade/GetLifecycle",
      "/api/admin/trade/ControlLifecycle",
      "/api/admin/strategy/ListStrategies",
      "/api/admin/strategy/CreateStrategy",
      "/api/admin/strategy/UpdateStrategy",
      "/api/admin/strategy/DeleteStrategy",
      "/api/admin/strategy/GetLifecycle",
      "/api/admin/strategy/ControlLifecycle",
      "/api/admin/factor-mgr/CreateFactor",
      "/api/admin/factor-mgr/UpdateFactor",
      "/api/admin/factor-mgr/DeleteFactor",
      "/api/admin/factor-mgr/ValidateFactorSpec",
      "/api/admin/factor-mgr/ListFactorFunctions",
      "/api/admin/factor-mgr/ListFactorCategories",
      "/api/admin/factor-mgr/UpsertFactorCategory",
      "/api/admin/factor-mgr/DeleteFactorCategory",
      "/api/admin/factor-mgr/SetFactorCategory",
      "/api/admin/factor-mgr/ImportFactorCatalog",
      "/api/admin/factor-mgr/PublishUdf",
      "/api/admin/factor-mgr/ListUdfs",
      "/api/admin/factor-mgr/GetUdf",
      "/api/admin/factor-mgr/ActivateUdf",
      "/api/admin/factor-mgr/GetStatus",
      "/api/admin/archive/GetLifecycle",
      "/api/admin/archive/ControlLifecycle"
    ]) {
      expect(spacePolicyForUrl(url), url).toBe("global");
    }

    for (const url of [
      "/api/admin/strategy/GetStrategy",
      "/api/admin/strategy/ValidateStrategy",
      "/api/admin/activity/ListActivityLogs",
      "/api/admin/factor-mgr/ListFactorTasks",
      "/api/admin/factor-mgr/GetFactor",
      "/api/admin/factor-mgr/ListFactors"
    ]) {
      expect(spacePolicyForUrl(url), url).toBe("space_optional");
    }

    for (const url of [
      "/api/admin/storage/ListDatasets",
      "/api/admin/collector/GetTaskList",
      "/api/admin/trade/ListOrders",
      "/api/admin/strategy/ListStrategyInstances",
      "/api/admin/factor-mgr/CreateFactorTask",
      "/api/admin/archive/ListArchiveTasks"
    ]) {
      expect(spacePolicyForUrl(url), url).toBe("space_required");
    }
  });

  it("never injects X-Space-Id, never waits for space init, and never cancels global endpoints on space switch", async () => {
    setSelectedSpaceIdCache("space-a");
    let initCalls = 0;
    setSpaceInitializer(async () => {
      initCalls += 1;
    });
    const handler = spaceHandler();
    for (const url of [
      "/api/admin/trade/InspectExchangeCredential",
      "/api/admin/trade/ListBlockedInstruments",
      "/api/admin/trade/ListDelists",
      "/api/admin/trade/UnblockInstrument",
      "/api/admin/strategy/ListStrategies",
      "/api/admin/collector/DeleteCloudAccount",
      "/api/admin/factor-mgr/ListFactorCategories",
      "/api/admin/archive/GetLifecycle"
    ]) {
      const prepared = await handler({ url, headers: new AxiosHeaders() } as InternalAxiosRequestConfig);
      expect(prepared.headers.get("X-Space-Id"), url).toBeUndefined();
      expect(prepared.injectedSpaceId, url).toBeUndefined();
    }

    const seen: Array<{ url: string; signal?: AbortSignal }> = [];
    const client = {
      interceptors: { request: { use: () => undefined }, response: { use: () => undefined } },
      post: (url: string, _data?: unknown, config?: { signal?: AbortSignal }) => {
        seen.push({ url, signal: config?.signal });
        return Promise.resolve({ ok: true });
      }
    };
    installSpaceAwareClient(client as never);
    await expect(client.post("/api/admin/trade/ListBlockedInstruments", {})).resolves.toEqual({ ok: true });
    await expect(client.post("/api/admin/trade/ListDelists", {})).resolves.toEqual({ ok: true });
    await expect(client.post("/api/admin/strategy/ListStrategies", {})).resolves.toEqual({ ok: true });
    setSelectedSpaceIdCache("space-b");
    for (const item of seen) expect(item.signal, item.url).toBeUndefined();
    expect(initCalls).toBe(0);
    setSpaceInitializer(null);
    setSelectedSpaceIdCache("");
  });

  it("lets space_optional requests run without blocking when no space is selected, forwards X-Space-Id when selected, and skips injection for global: true", async () => {
    setSelectedSpaceIdCache("");
    let initCalls = 0;
    setSpaceInitializer(async () => {
      initCalls += 1;
      setSelectedSpaceIdCache("crypto");
    });
    const handler = spaceHandler();

    // 无空间时不阻塞也不等空间初始化
    for (const url of [
      "/api/admin/strategy/GetStrategy",
      "/api/admin/strategy/ValidateStrategy",
      "/api/admin/activity/ListActivityLogs",
      "/api/admin/factor-mgr/ListFactorTasks"
    ]) {
      const prepared = await handler({ url, data: {}, headers: new AxiosHeaders() } as InternalAxiosRequestConfig);
      expect(prepared.headers.get("X-Space-Id"), url).toBeUndefined();
    }
    expect(initCalls).toBe(0);

    // 有选中空间时透传 X-Space-Id，但请求体声明 global: true 时不注入
    setSelectedSpaceIdCache("space-a");
    const withSpace = await handler({
      url: "/api/admin/strategy/GetStrategy",
      data: { strategy_id: "s1" },
      headers: new AxiosHeaders()
    } as InternalAxiosRequestConfig);
    expect(withSpace.headers.get("X-Space-Id")).toBe("space-a");
    expect(withSpace.injectedSpaceId).toBe("space-a");

    const globalActivity = await handler({
      url: "/api/admin/activity/ListActivityLogs",
      data: { global: true, page: { page: 1, size: 20 } },
      headers: new AxiosHeaders()
    } as InternalAxiosRequestConfig);
    expect(globalActivity.headers.get("X-Space-Id")).toBeUndefined();
    expect(globalActivity.injectedSpaceId).toBeUndefined();

    // 带了选中空间的 space_optional 读取在切空间时被取消，global: true 不被取消
    const seen: Array<{ url: string; signal?: AbortSignal }> = [];
    const client = {
      interceptors: { request: { use: () => undefined }, response: { use: () => undefined } },
      post: (url: string, _data?: unknown, config?: { signal?: AbortSignal }) => {
        seen.push({ url, signal: config?.signal });
        return new Promise(() => undefined);
      }
    };
    installSpaceAwareClient(client as never);
    const scopedRead = client.post("/api/admin/strategy/GetStrategy", { strategy_id: "s1" });
    void client.post("/api/admin/activity/ListActivityLogs", { global: true });
    const outcome = scopedRead.then(
      () => "resolved",
      error => (error instanceof StaleSpaceRequestError ? "stale" : "other")
    );
    setSelectedSpaceIdCache("space-b");
    expect(await outcome).toBe("stale");
    expect(seen[0].signal?.aborted).toBe(true);
    expect(seen[1].signal).toBeUndefined();

    setSpaceInitializer(null);
    setSelectedSpaceIdCache("");
  });
});
