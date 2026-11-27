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

import { useStrategyStore } from "./strategy";

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
    const latePage = deferred<{ items: unknown[]; page: { total: number; has_more?: boolean } }>();
    api.listStrategies.mockImplementation((params: { page?: number; size?: number }) =>
      params.size === 200 ? Promise.resolve({ items: all, page: { total: 21, has_more: false } }) : latePage.promise
    );
    const store = useStrategyStore();
    await store.loadAllStrategies(200);
    expect(store.strategyCatalog).toHaveLength(21);
    const paged = store.loadStrategies({ page: 1, size: 20 });
    latePage.resolve({ items: all.slice(0, 20), page: { total: 21, has_more: true } });
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
});
