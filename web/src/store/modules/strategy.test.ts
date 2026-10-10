import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";

const api = vi.hoisted(() => ({
  deleteInstance: vi.fn(),
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

import { useStrategyStore } from "./strategy";

const instance = (overrides: Record<string, unknown> = {}) => ({
  instance_id: "instance-1",
  strategy_id: "strategy-1",
  space_id: "space-1",
  view_id: "view_a",
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

describe("strategy store", () => {
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

  it("does not query history when the current instance has no session", async () => {
    api.getInstance.mockResolvedValue(instance({ session_id: "" }));
    api.listStrategyTargets.mockResolvedValue({ targets: [], session_id: "", bar_end_time: "", valid_until: "", result_id: "" });
    const store = useStrategyStore();
    await store.loadInstanceDetail("instance-1");
    expect(api.listStrategyResults).not.toHaveBeenCalled();
    expect(store.results).toEqual([]);
  });

  it("drops a late all-history response after switching back to a session-less current scope", async () => {
    api.getInstance.mockResolvedValue(instance({ session_id: "", enabled: false }));
    api.listStrategyTargets.mockResolvedValue({ targets: [] });
    const late = deferred<{ items: unknown[]; page: { total: number } }>();
    api.listStrategyResults.mockReturnValue(late.promise);
    const store = useStrategyStore();
    await store.loadInstanceDetail("instance-1");
    const history = store.loadResultPage(1, 20, true);
    await store.loadResultPage(1, 20, false);
    late.resolve({ items: [{ result_id: "old-session" }], page: { total: 1 } });
    await history;
    expect(store.results).toEqual([]);
    expect(store.totalResults).toBe(0);
  });

  it("keeps the full strategy catalog when a late paged list response arrives", async () => {
    const definition = (id: string) => ({
      strategy_id: id,
      name: id,
      dsl_yaml: "",
      dsl_hash: "h",
      created_at: "",
      updated_at: ""
    });
    const all = Array.from({ length: 21 }, (_, index) => definition(`s${index}`));
    const latePage = deferred<{ items: unknown[]; page: { total: number } }>();
    api.listStrategies.mockImplementation((params: { page?: number; page_size?: number }) =>
      params.page_size === 200 ? Promise.resolve({ items: all, page: { total: 21 } }) : latePage.promise
    );
    const store = useStrategyStore();
    await store.loadAllStrategies(200);
    expect(store.strategyCatalog).toHaveLength(21);
    const paged = store.loadStrategies({ page: 1, page_size: 20 });
    latePage.resolve({ items: all.slice(0, 20), page: { total: 21 } });
    await paged;
    expect(store.strategies).toHaveLength(20);
    expect(store.strategyCatalog).toHaveLength(21);
    expect(store.strategiesComplete).toBe(true);
  });

  it("invalidates the cached catalog and ignores an in-flight catalog request", async () => {
    const definition = (id: string) => ({
      strategy_id: id,
      name: id,
      dsl_yaml: "",
      dsl_hash: "h",
      created_at: "",
      updated_at: ""
    });
    const pending = deferred<{ items: unknown[]; page: { total: number } }>();
    api.listStrategies
      .mockReturnValueOnce(Promise.resolve({ items: [definition("s1")], page: { total: 1 } }))
      .mockReturnValueOnce(pending.promise);
    const store = useStrategyStore();
    await store.loadAllStrategies(200);
    expect(store.strategiesComplete).toBe(true);
    // 保存了新定义：目录作废；此时在途的旧目录请求返回，也不能把旧目录重新标成完整。
    const stale = store.loadAllStrategies(200);
    store.invalidateStrategyCatalog();
    expect(store.strategiesComplete).toBe(false);
    pending.resolve({ items: [definition("s1")], page: { total: 1 } });
    await stale;
    expect(store.strategiesComplete).toBe(false);
    expect(store.strategyCatalog).toHaveLength(1);
  });

  it("applies a fast target response without waiting for slow history", async () => {
    const targets = deferred<{ targets: []; session_id: string; bar_end_time: string; valid_until: string; result_id: string }>();
    const results = deferred<{ items: []; page: { total: number } }>();
    api.getInstance.mockResolvedValue(instance());
    api.listStrategyTargets.mockReturnValue(targets.promise);
    api.listStrategyResults.mockReturnValue(results.promise);
    const store = useStrategyStore();
    const request = store.loadInstanceDetail("instance-1");
    await Promise.resolve();
    await Promise.resolve();
    targets.resolve({
      targets: [],
      session_id: "session-1",
      bar_end_time: "2026-09-06T01:00:00Z",
      valid_until: "2026-09-06T02:00:00Z",
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
    const store = useStrategyStore();
    const stale = store.loadInstanceDetail("instance-1");
    const current = store.loadInstanceDetail("instance-2");
    await current;
    first.resolve(instance({ instance_id: "instance-1" }));
    await stale;
    expect(store.instance?.instance_id).toBe("instance-2");
  });

  it("keeps the detail page usable when the definition was deleted", async () => {
    api.getInstance.mockResolvedValue(instance());
    api.getStrategy.mockRejectedValue(new Error("not found"));
    api.listStrategyTargets.mockResolvedValue({
      targets: [],
      session_id: "session-1",
      bar_end_time: "",
      valid_until: "",
      result_id: ""
    });
    api.listStrategyResults.mockResolvedValue({ items: [], page: { total: 0 } });
    const store = useStrategyStore();
    await expect(store.loadInstanceDetail("instance-1")).resolves.toBe(true);
    expect(store.strategy).toBeNull();
    expect(store.detailError).toBe("");
  });

  it("caches result explanations and removes deleted rows", async () => {
    api.getStrategyResult.mockResolvedValue({ result: { result_id: "r1" }, items: [], dsl_yaml: "", resolved_json: "{}" });
    api.deleteInstance.mockResolvedValue(undefined);
    const store = useStrategyStore();
    await store.loadResultDetail("r1");
    await store.loadResultDetail("r1");
    expect(api.getStrategyResult).toHaveBeenCalledTimes(1);
    store.instances = [instance(), instance({ instance_id: "instance-2" })];
    store.totalInstances = 2;
    await store.removeInstance("instance-1");
    expect(store.instances.map(item => item.instance_id)).toEqual(["instance-2"]);
    expect(store.totalInstances).toBe(1);
  });
});
