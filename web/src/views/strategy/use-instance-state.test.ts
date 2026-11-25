import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";

const api = vi.hoisted(() => ({
  deleteStrategy: vi.fn(),
  getInstance: vi.fn(),
  getStrategy: vi.fn(),
  getStrategyResult: vi.fn(),
  listInstances: vi.fn(),
  listStrategies: vi.fn(),
  listStrategyResults: vi.fn(),
  listStrategyTargets: vi.fn(),
  setInstanceEnabled: vi.fn()
}));
vi.mock("@/api/strategy", () => api);

import { RequestError } from "@/api/http";
import { useStrategyStore } from "@/store/modules/strategy";
import { useInstanceDetail } from "./use-instance-detail";
import { useInstanceList } from "./use-instance-list";

const instance = (overrides: Record<string, unknown> = {}) => ({
  instance_id: "instance-1",
  strategy_id: "strategy-1",
  space_id: "space-1",
  factor_task_id: "ftask_a",
  logical_account_id: "",
  enabled: true,
  session_id: "session-1",
  resolved_json: "{}",
  health: "ok",
  created_at: "",
  updated_at: "",
  ...overrides
});

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

describe("实例详情与实例列表", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    Object.values(api).forEach(mock => mock.mockReset());
    api.getStrategy.mockResolvedValue({
      strategy_id: "strategy-1",
      name: "策略",
      dsl_yaml: "name: 策略",
      dsl_hash: "h",
      created_at: "",
      updated_at: ""
    });
  });

  it("后台刷新开始时不清错误条，成功后才清", async () => {
    api.getInstance.mockRejectedValueOnce(new Error("策略模块不可用"));
    const store = useInstanceDetail();
    await expect(store.loadInstanceDetail("instance-1")).rejects.toThrow();
    expect(store.detailError).toBe("策略模块不可用");

    const pending = deferred<ReturnType<typeof instance>>();
    api.getInstance.mockReturnValueOnce(pending.promise);
    api.listStrategyTargets.mockResolvedValue({ targets: [] });
    api.listStrategyResults.mockResolvedValue({ items: [], page: { total: 0 } });
    const silent = store.loadInstanceDetail("instance-1", 1, 20, false, { silent: true });
    expect(store.detailError).toBe("策略模块不可用");
    pending.resolve(instance());
    await silent;
    expect(store.detailError).toBe("");
  });

  it("does not query history when the current instance has no session", async () => {
    api.getInstance.mockResolvedValue(instance({ session_id: "" }));
    api.listStrategyTargets.mockResolvedValue({ targets: [], session_id: "", bar_end_time: "", valid_until: "", result_id: "" });
    const store = useInstanceDetail();
    await store.loadInstanceDetail("instance-1");
    expect(api.listStrategyResults).not.toHaveBeenCalled();
    expect(store.results).toEqual([]);
  });

  it("drops a late all-history response after switching back to a session-less current scope", async () => {
    api.getInstance.mockResolvedValue(instance({ session_id: "", enabled: false }));
    api.listStrategyTargets.mockResolvedValue({ targets: [] });
    const late = deferred<{ items: unknown[]; page: { total: number } }>();
    api.listStrategyResults.mockReturnValue(late.promise);
    const store = useInstanceDetail();
    await store.loadInstanceDetail("instance-1");
    const history = store.loadResultPage(1, 20, true);
    await store.loadResultPage(1, 20, false);
    late.resolve({ items: [{ result_id: "old-session" }], page: { total: 1 } });
    await history;
    expect(store.results).toEqual([]);
    expect(store.totalResults).toBe(0);
  });

  it("applies a fast target response without waiting for slow history", async () => {
    const targets = deferred<{ targets: []; session_id: string; bar_end_time: string; valid_until: string; result_id: string }>();
    const results = deferred<{ items: []; page: { total: number } }>();
    api.getInstance.mockResolvedValue(instance());
    api.listStrategyTargets.mockReturnValue(targets.promise);
    api.listStrategyResults.mockReturnValue(results.promise);
    const store = useInstanceDetail();
    const request = store.loadInstanceDetail("instance-1");
    await Promise.resolve();
    await Promise.resolve();
    targets.resolve({
      targets: [],
      session_id: "session-1",
      bar_end_time: "1788656400000",
      valid_until: "1788660000000",
      result_id: "r1"
    });
    await request;
    expect(store.targetSnapshot?.session_id).toBe("session-1");
    expect(store.results).toEqual([]);
    results.resolve({ items: [], page: { total: 0 } });
  });

  it("ignores a stale detail response after navigating to another instance", async () => {
    const first = deferred<ReturnType<typeof instance>>();
    api.getInstance.mockReturnValueOnce(first.promise).mockResolvedValueOnce(instance({ instance_id: "instance-2" }));
    api.listStrategyTargets.mockResolvedValue({ targets: [], session_id: "", bar_end_time: "", valid_until: "", result_id: "" });
    api.listStrategyResults.mockResolvedValue({ items: [], page: { total: 0 } });
    const store = useInstanceDetail();
    const stale = store.loadInstanceDetail("instance-1");
    const current = store.loadInstanceDetail("instance-2");
    await current;
    first.resolve(instance({ instance_id: "instance-1" }));
    await stale;
    expect(store.instance?.instance_id).toBe("instance-2");
  });

  const emptyTargets = {
    targets: [],
    session_id: "session-1",
    bar_end_time: "",
    valid_until: "",
    result_id: ""
  };

  it("marks the definition as missing only when the server confirms NOT_FOUND", async () => {
    api.getInstance.mockResolvedValue(instance());
    api.getStrategy.mockRejectedValue(new RequestError("not found", { ret_info: { code: 5, msg: "not found" } } as never));
    api.listStrategyTargets.mockResolvedValue(emptyTargets);
    api.listStrategyResults.mockResolvedValue({ items: [], page: { total: 0 } });
    const store = useInstanceDetail();
    await expect(store.loadInstanceDetail("instance-1")).resolves.toBe(true);
    expect(store.strategy).toBeNull();
    expect(store.strategyStatus).toBe("missing");
    expect(store.detailError).toBe("");
  });

  it("reports a failed definition read as an error instead of deleted", async () => {
    api.getInstance.mockResolvedValue(instance());
    api.getStrategy.mockRejectedValue(new Error("network down"));
    api.listStrategyTargets.mockResolvedValue(emptyTargets);
    api.listStrategyResults.mockResolvedValue({ items: [], page: { total: 0 } });
    const store = useInstanceDetail();
    await expect(store.loadInstanceDetail("instance-1")).resolves.toBe(true);
    expect(store.strategy).toBeNull();
    expect(store.strategyStatus).toBe("error");
    expect(store.detailError).toBe("");
  });

  describe("history results", () => {
    const targets = { targets: [], session_id: "session-1", bar_end_time: "", valid_until: "", result_id: "" };
    const row = (id: string) => ({ result_id: id });

    async function loaded() {
      api.getInstance.mockResolvedValue(instance());
      api.getStrategy.mockResolvedValue({ strategy_id: "strategy-1" });
      api.listStrategyTargets.mockResolvedValue(targets);
      api.listStrategyResults.mockResolvedValue({ items: [row("r1"), row("r2")], page: { total: 45 } });
      const store = useInstanceDetail();
      await store.loadInstanceDetail("instance-1");
      await vi.waitFor(() => expect(store.resultsLoading).toBe(false));
      return store;
    }

    it("reports a failed result read without calling the instance detail failed", async () => {
      api.getInstance.mockResolvedValue(instance());
      api.getStrategy.mockResolvedValue({ strategy_id: "strategy-1" });
      api.listStrategyTargets.mockResolvedValue(targets);
      api.listStrategyResults.mockRejectedValue(new Error("结果服务超时"));
      const store = useInstanceDetail();
      await expect(store.loadInstanceDetail("instance-1")).resolves.toBe(true);
      await vi.waitFor(() => expect(store.resultsLoading).toBe(false));
      expect(store.resultsError).toBe("结果服务超时");
      expect(store.detailError).toBe("");
    });

    it("keeps the loaded rows and page when a page read fails", async () => {
      const store = await loaded();
      expect(store.loadedResultPage).toBe(1);
      api.listStrategyResults.mockRejectedValueOnce(new Error("翻页失败"));
      await expect(store.loadResultPage(2)).rejects.toThrow("翻页失败");
      expect(store.results.map(item => item.result_id)).toEqual(["r1", "r2"]);
      expect(store.loadedResultPage).toBe(1);
      expect(store.resultsError).toBe("翻页失败");
      expect(store.resultsLoading).toBe(false);
    });

    it("tracks loading while a page is read and records the loaded page and scope on success", async () => {
      const store = await loaded();
      const pending = deferred<{ items: unknown[]; page: { total: number } }>();
      api.listStrategyResults.mockReturnValueOnce(pending.promise);
      const reading = store.loadResultPage(3, 20, true);
      expect(store.resultsLoading).toBe(true);
      pending.resolve({ items: [row("r9")], page: { total: 45 } });
      await reading;
      expect(store.resultsLoading).toBe(false);
      expect(store.resultsError).toBe("");
      expect(store.loadedResultPage).toBe(3);
      expect(store.loadedResultAllHistory).toBe(true);
    });

    it("clears the previous error when a later read succeeds", async () => {
      const store = await loaded();
      api.listStrategyResults.mockRejectedValueOnce(new Error("失败"));
      await store.loadResultPage(2).catch(() => undefined);
      await store.loadResultPage(2);
      expect(store.resultsError).toBe("");
      expect(store.loadedResultPage).toBe(2);
    });

    it("keeps the previous rows when a background refresh fails", async () => {
      const store = await loaded();
      api.listStrategyResults.mockRejectedValueOnce(new Error("后台刷新失败"));
      await store.loadInstanceDetail("instance-1");
      await vi.waitFor(() => expect(store.resultsError).toBe("后台刷新失败"));
      expect(store.results).toHaveLength(2);
      expect(store.resultsLoading).toBe(false);
    });
  });

  it("tracks instance list and catalog loading and errors independently", async () => {
    const instances = deferred<{ items: unknown[]; page: { total: number } }>();
    api.listInstances.mockReturnValue(instances.promise);
    api.listStrategies.mockRejectedValue(new Error("目录失败"));
    const store = useStrategyStore();
    const list = useInstanceList();
    const loadingInstances = list.loadInstances({ page: 1, page_size: 20 });
    await expect(store.loadAllStrategies(200)).rejects.toThrow("目录失败");
    // 目录请求先结束并失败：不能关闭实例列表的加载状态，也不能把错误算到实例头上。
    expect(store.catalogLoading).toBe(false);
    expect(store.catalogError).toBe("目录失败");
    expect(list.instancesLoading).toBe(true);
    expect(list.instancesError).toBe("");
    instances.resolve({ items: [], page: { total: 0 } });
    await loadingInstances;
    expect(list.instancesLoading).toBe(false);
  });

  it("clears the instance list and drops an in-flight response when the space changes", async () => {
    const pending = deferred<{ items: unknown[]; page: { total: number } }>();
    api.listInstances.mockReturnValue(pending.promise);
    const store = useInstanceList();
    store.instances = [instance({ instance_id: "old" })] as never;
    store.totalInstances = 1;
    const stale = store.loadInstances({ page: 1, page_size: 20 });
    store.clearInstances();
    expect(store.instances).toEqual([]);
    expect(store.instancesLoading).toBe(false);
    pending.resolve({ items: [instance({ instance_id: "late" })], page: { total: 1 } });
    await stale;
    expect(store.instances).toEqual([]);
    expect(store.totalInstances).toBe(0);
  });
});
