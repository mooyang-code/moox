import { afterEach, describe, expect, it } from "vitest";
import { setSelectedSpaceIdCache } from "./space-header";
import { StaleSpaceRequestError } from "./stale-space";
import { runWithSpaceContext, setSpaceInitializer, spacePolicyForUrl } from "./space-client";

afterEach(() => {
  setSpaceInitializer(null);
  setSelectedSpaceIdCache("");
});

describe("runWithSpaceContext", () => {
  it("空间内请求自动注入当前选中空间，不附带任何签名头或状态污染", async () => {
    setSelectedSpaceIdCache("space-1");
    let capturedSpace: string | undefined;
    await runWithSpaceContext({ url: "/api/admin/collector/GetTask", data: {} }, async ctx => {
      capturedSpace = ctx.spaceId;
      return { ok: true };
    });
    expect(capturedSpace).toBe("space-1");
  });

  it("未选空间时 space_required 请求等待空间初始化完成，并发请求共用同一次初始化", async () => {
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
    const seen: Array<string | undefined> = [];
    const run = () =>
      runWithSpaceContext({ url: "/api/admin/trade/ListOrders", data: {} }, async ctx => {
        seen.push(ctx.spaceId);
        return ctx.spaceId;
      });
    const first = run();
    const second = run();
    await Promise.resolve();
    finish();
    await expect(Promise.all([first, second])).resolves.toEqual(["crypto", "crypto"]);
    expect(calls).toBe(1);
    expect(seen).toEqual(["crypto", "crypto"]);
  });

  it("全局接口永不等待空间初始化、不注入空间头，也不会被切空间取消", async () => {
    let initCalls = 0;
    setSpaceInitializer(async () => {
      initCalls += 1;
    });
    setSelectedSpaceIdCache("space-a");
    for (const url of [
      "/api/admin/auth/Login",
      "/api/admin/storage/ListSpaces",
      "/api/admin/storage/CreateSpace",
      "/api/admin/trade/InspectExchangeCredential",
      "/api/admin/trade/ListBlockedInstruments",
      "/api/admin/trade/ListDelists",
      "/api/admin/trade/UnblockInstrument",
      "/api/admin/strategy/ListStrategies",
      "/api/admin/collector/DeleteCloudAccount",
      "/api/admin/factor-mgr/ListFactorCategories"
    ]) {
      let seenCtx: { spaceId?: string; signal?: AbortSignal } = {};
      await runWithSpaceContext({ url, data: {} }, async ctx => {
        seenCtx = ctx;
        return { ok: true };
      });
      expect(seenCtx.spaceId, url).toBeUndefined();
      expect(seenCtx.signal, url).toBeUndefined();
    }
    expect(initCalls).toBe(0);
  });

  it("切空间时立即取消在途的空间内只读请求，但保留写入、全局读取与显式空间请求", async () => {
    setSelectedSpaceIdCache("space-a");
    const seen: Array<{ url: string; spaceId?: string; signal?: AbortSignal }> = [];
    const hang = (url: string, explicitSpaceId?: string) =>
      runWithSpaceContext({ url, data: {}, explicitSpaceId }, ctx => {
        seen.push({ url, spaceId: ctx.spaceId, signal: ctx.signal });
        return new Promise(() => undefined);
      });

    const read = hang("/api/admin/collector/GetTask");
    void hang("/api/admin/sysdeploy/GetCatalog");
    void hang("/api/admin/collector/ListCloudAccounts");
    void hang("/api/admin/collector/CreateTask");
    void hang("/api/admin/collector/GetTask", "other-space");

    const outcome = read.then(
      () => "resolved",
      error => (error instanceof StaleSpaceRequestError ? "stale" : "other")
    );
    setSelectedSpaceIdCache("space-b");
    expect(await outcome).toBe("stale");
    expect(seen[0].signal?.aborted).toBe(true);
    for (const exempt of seen.slice(1)) expect(exempt.signal).toBeUndefined();
    expect(seen[4].spaceId).toBe("other-space");
  });

  it("切走又切回原空间（A→B→A）时，旧代次的迟到响应与错误同样被判为 StaleSpaceRequestError", async () => {
    setSelectedSpaceIdCache("space-a");
    let resolveRead!: (value: string) => void;
    const read = runWithSpaceContext({ url: "/api/admin/collector/GetTask", data: {} }, () => {
      return new Promise<string>(resolve => {
        resolveRead = resolve;
      });
    });
    setSelectedSpaceIdCache("space-b");
    setSelectedSpaceIdCache("space-a");
    resolveRead("stale");
    await expect(read).rejects.toBeInstanceOf(StaleSpaceRequestError);
  });

  it("在空间初始化完成前发起的空间读取，在初始化完成后切空间仍会被取消", async () => {
    let finishInit!: () => void;
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
    const read = runWithSpaceContext({ url: "/api/admin/collector/GetTask", data: {} }, ctx => {
      seen.push({ signal: ctx.signal });
      return new Promise(() => undefined);
    });
    const outcome = read.then(
      () => "resolved",
      error => (error instanceof StaleSpaceRequestError ? "stale" : "other")
    );
    finishInit();
    await new Promise(resolve => setTimeout(resolve, 0));
    expect(seen).toHaveLength(1);
    setSelectedSpaceIdCache("space-b");
    expect(await outcome).toBe("stale");
    expect(seen[0].signal?.aborted).toBe(true);
  });

  it("按服务目录规则将接口归为 global、space_optional 与 space_required", () => {
    for (const url of [
      "/api/admin/auth/Login",
      "/api/admin/ssh/CreateSession",
      "/api/admin/secret/ListSecrets",
      "/api/admin/sysdeploy/GetCatalog",
      "/api/admin/monitor/GetHealthOverview",
      "/api/admin/storage/ListSpaces",
      "/api/admin/storage/CreateSpace",
      "/api/admin/storage/UpdateSpace",
      "/api/admin/storage/DeleteSpace",
      "/api/admin/collector/ListCloudAccounts",
      "/api/admin/collector/CreateCloudAccount",
      "/api/admin/collector/UpdateCloudAccount",
      "/api/admin/collector/ListCloudRegions",
      "/api/admin/collector/DeleteCloudAccount",
      "/api/admin/collector/InitPackageUpload",
      "/api/admin/collector/CompletePackageUpload",
      "/api/admin/collector/ListFunctionPackages",
      "/api/admin/collector/DeleteFunctionPackage",
      "/api/admin/collector/GetDataTypeConfigs",
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
      "/api/admin/factor-mgr/GetStatus"
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
      "/api/admin/archive/ListArchiveTasks",
      "/api/admin/archive/ClearArchiveUnrecoverable"
    ]) {
      expect(spacePolicyForUrl(url), url).toBe("space_required");
    }
  });

  it("space_optional 无空间时不阻塞，有空间时透传并在切空间时取消，global: true 时不注入也不取消", async () => {
    let initCalls = 0;
    setSpaceInitializer(async () => {
      initCalls += 1;
      setSelectedSpaceIdCache("crypto");
    });

    for (const url of [
      "/api/admin/strategy/GetStrategy",
      "/api/admin/strategy/ValidateStrategy",
      "/api/admin/activity/ListActivityLogs",
      "/api/admin/factor-mgr/ListFactorTasks"
    ]) {
      let seenSpace: string | undefined;
      await runWithSpaceContext({ url, data: {} }, async ctx => {
        seenSpace = ctx.spaceId;
        return {};
      });
      expect(seenSpace, url).toBeUndefined();
    }
    expect(initCalls).toBe(0);

    setSelectedSpaceIdCache("space-a");
    const seen: Array<{ spaceId?: string; signal?: AbortSignal }> = [];
    const scopedRead = runWithSpaceContext({ url: "/api/admin/strategy/GetStrategy", data: { strategy_id: "s1" } }, ctx => {
      seen.push(ctx);
      return new Promise(() => undefined);
    });
    void runWithSpaceContext({ url: "/api/admin/activity/ListActivityLogs", data: { global: true } }, ctx => {
      seen.push(ctx);
      return new Promise(() => undefined);
    });
    expect(seen[0].spaceId).toBe("space-a");
    expect(seen[1].spaceId).toBeUndefined();

    const outcome = scopedRead.then(
      () => "resolved",
      error => (error instanceof StaleSpaceRequestError ? "stale" : "other")
    );
    setSelectedSpaceIdCache("space-b");
    expect(await outcome).toBe("stale");
    expect(seen[0].signal?.aborted).toBe(true);
    expect(seen[1].signal).toBeUndefined();
  });
});
