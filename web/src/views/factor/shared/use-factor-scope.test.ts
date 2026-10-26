import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { defineComponent, h, KeepAlive, nextTick, ref } from "vue";
import { flushPromises, mount } from "@vue/test-utils";
import type { FactorTaskInfo } from "@/api/factor/types";

const hoisted = await vi.hoisted(async () => {
  const { reactive } = await import("vue");
  return {
    route: reactive({ path: "/factor/tasks", name: "factor-tasks", query: {} as Record<string, string | undefined> }),
    replace: vi.fn(),
    usePolling: vi.fn()
  };
});

vi.mock("vue-router", () => ({
  useRoute: () => hoisted.route,
  useRouter: () => ({ replace: hoisted.replace })
}));
vi.mock("@/hooks/usePolling", () => ({ usePolling: hoisted.usePolling }));
vi.mock("@/api/factor", () => ({ listFactorTasks: vi.fn(), getFactorStatus: vi.fn().mockResolvedValue(null) }));
vi.mock("@/api/storage/metadata", () => ({ listDatasets: vi.fn() }));
vi.mock("@/api/admin/spaces", () => ({ listSpaces: vi.fn() }));
vi.mock("@/api/admin/space-header", async importOriginal => ({
  ...(await importOriginal<typeof import("@/api/admin/space-header")>()),
  setSelectedSpaceIdCache: vi.fn()
}));

import { listFactorTasks } from "@/api/factor";
import { listDatasets } from "@/api/storage/metadata";
import { useFactorStore } from "@/store/modules/factor";
import { useSpaceStore } from "@/store/modules/space";
import { buildTabQuery, useFactorScope } from "./use-factor-scope";

const info = (taskId: string, spaceId = "crypto"): FactorTaskInfo => ({
  factor_task: {
    task_id: taskId,
    space_id: spaceId,
    source_dataset_id: "d",
    freq: "1m",
    result_dataset_id: `r_${taskId}`,
    status: "enabled"
  },
  members: []
});

const sets = (items: FactorTaskInfo[]) => ({
  ret_info: { code: 0, msg: "" },
  factor_tasks: items,
  page_result: { page: 1, size: 500, total: items.length, has_more: false, next_cursor: "" }
});

function mountScope(options: Parameters<typeof useFactorScope>[0] = {}) {
  let scope!: ReturnType<typeof useFactorScope>;
  const wrapper = mount(
    defineComponent({
      setup() {
        scope = useFactorScope(options);
        return () => null;
      }
    })
  );
  return { wrapper, scope };
}

describe("useFactorScope", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    hoisted.route.query = {};
    hoisted.route.path = "/factor/tasks";
    hoisted.replace.mockReset();
    hoisted.usePolling.mockReset();
    vi.mocked(listFactorTasks).mockReset();
    vi.mocked(listDatasets).mockReset();
    vi.mocked(listDatasets).mockResolvedValue({
      ret_info: { code: 0, msg: "" },
      datasets: [],
      page_result: { page: 1, size: 500, total: 0, has_more: false, next_cursor: "" }
    } as never);
    useSpaceStore().selectedSpaceId = "crypto";
  });

  it("does not reset the store when remounted for the same space", async () => {
    vi.mocked(listFactorTasks).mockResolvedValue(sets([info("a")]));
    const store = useFactorStore();
    await store.load("crypto");
    const reset = vi.spyOn(store, "reset");
    vi.mocked(listFactorTasks).mockClear();

    mountScope();
    await flushPromises();
    expect(reset).not.toHaveBeenCalled();
    expect(listFactorTasks).not.toHaveBeenCalled();
    expect(store.sets).toHaveLength(1);
  });

  it("resets and reloads when the space id changes", async () => {
    vi.mocked(listFactorTasks)
      .mockResolvedValueOnce(sets([info("a")]))
      .mockResolvedValueOnce(sets([info("x", "stockcn")]));
    const store = useFactorStore();
    mountScope();
    await flushPromises();
    expect(store.sets.map(item => item.factor_task.task_id)).toEqual(["a"]);

    const reset = vi.spyOn(store, "reset");
    useSpaceStore().selectedSpaceId = "stockcn";
    await flushPromises();
    expect(reset).toHaveBeenCalledTimes(1);
    expect(store.sets.map(item => item.factor_task.task_id)).toEqual(["x"]);
  });

  it("requireSet resolves ?task= then currentTaskId then first", async () => {
    vi.mocked(listFactorTasks).mockResolvedValue(sets([info("a"), info("b"), info("c")]));
    const store = useFactorStore();

    hoisted.route.query = { task: "c" };
    mountScope({ requireSet: true });
    await flushPromises();
    expect(store.currentTaskId).toBe("c");
  });

  it("falls back to the last used set and then the first one", async () => {
    vi.mocked(listFactorTasks).mockResolvedValue(sets([info("a"), info("b")]));
    const store = useFactorStore();
    await store.load("crypto");
    store.select("b");
    hoisted.route.query = { task: "missing" };
    mountScope({ requireSet: true });
    await flushPromises();
    expect(store.currentTaskId).toBe("b");

    store.reset();
    hoisted.route.query = {};
    mountScope({ requireSet: true });
    await flushPromises();
    expect(store.currentTaskId).toBe("a");
  });

  it("writes the resolved set back with router.replace", async () => {
    vi.mocked(listFactorTasks).mockResolvedValue(sets([info("a"), info("b")]));
    hoisted.route.query = { tab: "recalc" };
    mountScope({ requireSet: true });
    await flushPromises();
    expect(hoisted.replace).toHaveBeenCalledWith({ path: "/factor/tasks", query: { tab: "recalc", task: "a" } });
  });

  it("optional scope leaves ?task= untouched", async () => {
    vi.mocked(listFactorTasks).mockResolvedValue(sets([info("a")]));
    const store = useFactorStore();
    mountScope({ requireSet: false });
    await flushPromises();
    expect(hoisted.replace).not.toHaveBeenCalled();
    expect(store.sets).toHaveLength(1);
  });

  it("follows ?task= changes while active", async () => {
    vi.mocked(listFactorTasks).mockResolvedValue(sets([info("a"), info("b")]));
    const store = useFactorStore();
    mountScope({ requireSet: true });
    await flushPromises();
    hoisted.replace.mockClear();
    hoisted.route.query = { task: "b" };
    await nextTick();
    expect(store.currentTaskId).toBe("b");
  });

  it("re-syncs ?task= changed while deactivated when the page is activated again", async () => {
    vi.mocked(listFactorTasks).mockResolvedValue(sets([info("a"), info("b")]));
    const store = useFactorStore();
    hoisted.route.query = { tab: "results", task: "a" };
    const Scoped = defineComponent({
      setup() {
        useFactorScope({ requireSet: true, poll: false });
        return () => null;
      }
    });
    const shown = ref(true);
    mount(defineComponent({ setup: () => () => h(KeepAlive, null, shown.value ? h(Scoped) : null) }));
    await flushPromises();
    expect(store.currentTaskId).toBe("a");

    shown.value = false;
    await nextTick();
    hoisted.route.query = { tab: "results", task: "b" };
    await nextTick();
    expect(store.currentTaskId).toBe("a");

    shown.value = true;
    await flushPromises();
    expect(store.currentTaskId).toBe("b");
  });

  it("selectTask drops the previous set's job and factor conditions", async () => {
    vi.mocked(listFactorTasks).mockResolvedValue(sets([info("a"), info("b")]));
    hoisted.route.query = { tab: "results", task: "a", fc: "x" };
    const { scope } = mountScope({ requireSet: true });
    await flushPromises();
    hoisted.replace.mockClear();
    scope.selectTask("b");
    expect(hoisted.replace).toHaveBeenCalledWith({ path: "/factor/tasks", query: { tab: "results", task: "b" } });
  });

  it("drops the previous set's factor conditions even when the store change lands before the route update", async () => {
    vi.mocked(listFactorTasks).mockResolvedValue(sets([info("a"), info("b")]));
    hoisted.route.query = { tab: "results", task: "a", fc: "x" };
    const { scope } = mountScope({ requireSet: true });
    await flushPromises();
    hoisted.replace.mockClear();
    // 路由替换是异步的：selectTask 之后 route.query 仍是旧值，store 监听随后又发起一次跳转
    scope.selectTask("b");
    await nextTick();
    for (const [target] of hoisted.replace.mock.calls) expect(target.query.fc).toBeUndefined();
    expect(hoisted.replace.mock.calls.length).toBeGreaterThan(0);
  });

  it("polls through usePolling only while a space is selected", async () => {
    vi.mocked(listFactorTasks).mockResolvedValue(sets([info("a")]));
    mountScope();
    await flushPromises();
    expect(hoisted.usePolling).toHaveBeenCalledTimes(1);
    const [, interval, active] = hoisted.usePolling.mock.calls[0];
    expect(interval).toBe(10_000);
    expect(active()).toBe(true);
    useSpaceStore().selectedSpaceId = "";
    expect(active()).toBe(false);
  });

  it("can opt out of polling", async () => {
    vi.mocked(listFactorTasks).mockResolvedValue(sets([info("a")]));
    mountScope({ poll: false });
    await flushPromises();
    expect(hoisted.usePolling).not.toHaveBeenCalled();
  });
});

describe("buildTabQuery", () => {
  it("tab switch keeps task and drops unrelated detail/job", () => {
    expect(buildTabQuery({ task: "a", detail: "a", job: "j1" }, "results")).toEqual({ tab: "results", task: "a" });
    expect(buildTabQuery({ task: "a", detail: "a", job: "j1" }, "recalc")).toEqual({ tab: "recalc", task: "a", job: "j1" });
    expect(buildTabQuery({ task: "a", detail: "a", job: "j1" }, "tasks")).toEqual({ task: "a", detail: "a" });
  });
});
