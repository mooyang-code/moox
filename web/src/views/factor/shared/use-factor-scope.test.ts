import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { defineComponent, nextTick } from "vue";
import { flushPromises, mount } from "@vue/test-utils";
import type { FactorSetInfo } from "@/api/factor/types";

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
vi.mock("@/api/factor", () => ({ listFactorSets: vi.fn(), getFactorStatus: vi.fn().mockResolvedValue(null) }));
vi.mock("@/api/storage/metadata", () => ({ listDatasets: vi.fn() }));
vi.mock("@/api/admin/spaces", () => ({ listSpaces: vi.fn() }));
vi.mock("@/api/admin/space-header", () => ({ setSelectedSpaceIdCache: vi.fn() }));

import { listFactorSets } from "@/api/factor";
import { listDatasets } from "@/api/storage/metadata";
import { useFactorStore } from "@/store/modules/factor";
import { useSpaceStore } from "@/store/modules/space";
import { buildTabQuery, useFactorScope } from "./use-factor-scope";

const info = (setId: string, spaceId = "crypto"): FactorSetInfo => ({
  factor_set: {
    set_id: setId,
    space_id: spaceId,
    source_dataset_id: "d",
    freq: "1m",
    subject_mode: "all",
    subjects: [],
    result_dataset_id: `r_${setId}`,
    status: "enabled"
  },
  members: []
});

const sets = (items: FactorSetInfo[]) => ({
  ret_info: { code: 0, msg: "" },
  factor_sets: items,
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
    vi.mocked(listFactorSets).mockReset();
    vi.mocked(listDatasets).mockReset();
    vi.mocked(listDatasets).mockResolvedValue({
      ret_info: { code: 0, msg: "" },
      datasets: [],
      page_result: { page: 1, size: 500, total: 0, has_more: false, next_cursor: "" }
    } as never);
    useSpaceStore().selectedSpaceId = "crypto";
  });

  it("does not reset the store when remounted for the same space", async () => {
    vi.mocked(listFactorSets).mockResolvedValue(sets([info("a")]));
    const store = useFactorStore();
    await store.load("crypto");
    const reset = vi.spyOn(store, "reset");
    vi.mocked(listFactorSets).mockClear();

    mountScope();
    await flushPromises();
    expect(reset).not.toHaveBeenCalled();
    expect(listFactorSets).not.toHaveBeenCalled();
    expect(store.sets).toHaveLength(1);
  });

  it("resets and reloads when the space id changes", async () => {
    vi.mocked(listFactorSets)
      .mockResolvedValueOnce(sets([info("a")]))
      .mockResolvedValueOnce(sets([info("x", "stockcn")]));
    const store = useFactorStore();
    mountScope();
    await flushPromises();
    expect(store.sets.map(item => item.factor_set.set_id)).toEqual(["a"]);

    const reset = vi.spyOn(store, "reset");
    useSpaceStore().selectedSpaceId = "stockcn";
    await flushPromises();
    expect(reset).toHaveBeenCalledTimes(1);
    expect(store.sets.map(item => item.factor_set.set_id)).toEqual(["x"]);
  });

  it("requireSet resolves ?set= then currentSetId then first", async () => {
    vi.mocked(listFactorSets).mockResolvedValue(sets([info("a"), info("b"), info("c")]));
    const store = useFactorStore();

    hoisted.route.query = { set: "c" };
    mountScope({ requireSet: true });
    await flushPromises();
    expect(store.currentSetId).toBe("c");
  });

  it("falls back to the last used set and then the first one", async () => {
    vi.mocked(listFactorSets).mockResolvedValue(sets([info("a"), info("b")]));
    const store = useFactorStore();
    await store.load("crypto");
    store.select("b");
    hoisted.route.query = { set: "missing" };
    mountScope({ requireSet: true });
    await flushPromises();
    expect(store.currentSetId).toBe("b");

    store.reset();
    hoisted.route.query = {};
    mountScope({ requireSet: true });
    await flushPromises();
    expect(store.currentSetId).toBe("a");
  });

  it("writes the resolved set back with router.replace", async () => {
    vi.mocked(listFactorSets).mockResolvedValue(sets([info("a"), info("b")]));
    hoisted.route.query = { tab: "recalc" };
    mountScope({ requireSet: true });
    await flushPromises();
    expect(hoisted.replace).toHaveBeenCalledWith({ path: "/factor/tasks", query: { tab: "recalc", set: "a" } });
  });

  it("optional scope leaves ?set= untouched", async () => {
    vi.mocked(listFactorSets).mockResolvedValue(sets([info("a")]));
    const store = useFactorStore();
    mountScope({ requireSet: false });
    await flushPromises();
    expect(hoisted.replace).not.toHaveBeenCalled();
    expect(store.sets).toHaveLength(1);
  });

  it("follows ?set= changes while active", async () => {
    vi.mocked(listFactorSets).mockResolvedValue(sets([info("a"), info("b")]));
    const store = useFactorStore();
    mountScope({ requireSet: true });
    await flushPromises();
    hoisted.replace.mockClear();
    hoisted.route.query = { set: "b" };
    await nextTick();
    expect(store.currentSetId).toBe("b");
  });

  it("polls through usePolling only while a space is selected", async () => {
    vi.mocked(listFactorSets).mockResolvedValue(sets([info("a")]));
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
    vi.mocked(listFactorSets).mockResolvedValue(sets([info("a")]));
    mountScope({ poll: false });
    await flushPromises();
    expect(hoisted.usePolling).not.toHaveBeenCalled();
  });
});

describe("buildTabQuery", () => {
  it("tab switch keeps set and drops unrelated detail/job", () => {
    expect(buildTabQuery({ set: "a", detail: "a", job: "j1" }, "results")).toEqual({ tab: "results", set: "a" });
    expect(buildTabQuery({ set: "a", detail: "a", job: "j1" }, "recalc")).toEqual({ tab: "recalc", set: "a", job: "j1" });
    expect(buildTabQuery({ set: "a", detail: "a", job: "j1" }, "tasks")).toEqual({ set: "a", detail: "a" });
  });
});
