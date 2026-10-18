import { flushPromises, mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h, nextTick, reactive } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";

const ctx = vi.hoisted(() => ({ store: null as any, space: null as any, listInstances: vi.fn() }));
vi.mock("@/api/strategy", () => ({ listInstances: ctx.listInstances }));
vi.mock("@/store/modules/space", () => ({ useSpaceStore: () => ctx.space }));
vi.mock("@/store/modules/strategy", () => ({ useStrategyStore: () => ctx.store }));
vi.mock("@/views/strategy/components/strategy-instance-results.vue", () => ({
  default: defineComponent({
    props: ["instance", "targetState"],
    setup(props) {
      return () => h("div", { class: "panel" }, `panel:${props.instance.instance_id}`);
    }
  })
}));

import ResultsPage from "./index.vue";

function instance(id: string, overrides: Record<string, unknown> = {}) {
  return {
    instance_id: id,
    strategy_id: "s1",
    space_id: "crypto",
    view_id: "v",
    logical_account_id: "",
    enabled: true,
    session_id: "sess",
    resolved_json: "{}",
    health: "ok",
    created_at: "",
    updated_at: "2026-10-01T00:00:00Z",
    ...overrides
  };
}

const stubs = {
  "a-alert": { template: '<div class="alert"><slot /></div>' },
  "a-empty": { props: ["description"], template: '<div class="empty">{{ description }}<slot name="extra" /></div>' },
  "a-space": { template: "<div><slot /></div>" },
  "a-button": { template: "<button><slot /></button>" },
  "a-switch": true,
  "a-tag": true,
  "a-link": { template: "<a><slot /></a>" },
  "a-spin": true,
  "a-tabs": {
    props: ["activeKey"],
    emits: ["change"],
    template: '<div class="tabs" :data-active="activeKey"><slot /></div>'
  },
  "a-tab-pane": { props: ["title"], template: '<span class="tab-title">{{ title }}</span>' },
  "icon-refresh": true,
  FactorInfoTip: true,
  StrategyStatusBadge: true
};

async function setup(query: Record<string, string>) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/strategy/tasks", name: "strategy-tasks", component: ResultsPage },
      { path: "/strategy/detail/:instanceId", name: "strategy-detail", component: { template: "<div />" } }
    ]
  });
  await router.push({ path: "/strategy/tasks", query: { tab: "results", ...query } });
  const wrapper = mount(ResultsPage, { global: { plugins: [router], stubs } });
  await flushPromises();
  return { router, wrapper };
}

describe("strategy results tab", () => {
  beforeEach(() => {
    ctx.space = reactive({ selectedSpaceId: "crypto" });
    ctx.listInstances.mockReset().mockResolvedValue({
      items: [instance("off", { enabled: false }), instance("on-1"), instance("on-2", { updated_at: "2026-10-05T00:00:00Z" })],
      page: { total: 3 }
    });
    ctx.store = reactive({
      instance: null as any,
      strategy: null,
      targetSnapshot: null,
      results: [],
      totalResults: 0,
      detailLoading: false,
      detailError: "",
      resultsLoading: false,
      resultsError: "",
      loadedResultPage: 1,
      loadedResultAllHistory: false,
      strategyCatalog: [{ strategy_id: "s1", name: "动量" }],
      strategiesComplete: true,
      loadAllStrategies: vi.fn(),
      loadInstanceDetail: vi.fn(async (id: string) => {
        ctx.store.instance = instance(id);
        return true;
      }),
      loadResultPage: vi.fn(),
      startPolling: vi.fn(),
      stopPolling: vi.fn(),
      clearDetail: vi.fn(() => {
        ctx.store.instance = null;
      })
    });
  });

  it("selects the first enabled, most recently updated instance and shows its results right away", async () => {
    const { router, wrapper } = await setup({});
    expect(router.currentRoute.value.query).toEqual({ tab: "results", instance: "on-2" });
    expect(ctx.store.loadInstanceDetail).toHaveBeenCalledWith("on-2", 1, 20, false);
    expect(wrapper.find(".panel").text()).toBe("panel:on-2");
    expect(wrapper.findAll(".tab-title").map(item => item.text())).toEqual(["动量 · on-2", "动量 · on-1", "动量 · off（停用）"]);
  });

  it("keeps an explicit instance from the URL, even one that is not in the list", async () => {
    const { router, wrapper } = await setup({ instance: "deleted-1" });
    expect(router.currentRoute.value.query.instance).toBe("deleted-1");
    expect(ctx.store.loadInstanceDetail).toHaveBeenCalledWith("deleted-1", 1, 20, false);
    expect(wrapper.findAll(".tab-title")[0].text()).toBe("实例 deleted-");
  });

  it("switches instances through the URL", async () => {
    const { router, wrapper } = await setup({ instance: "on-1" });
    await router.push({ path: "/strategy/tasks", query: { tab: "results", instance: "off" } });
    await flushPromises();
    expect(ctx.store.loadInstanceDetail).toHaveBeenLastCalledWith("off", 1, 20, false);
    expect(wrapper.find(".panel").text()).toBe("panel:off");
  });

  it("clears the old space's instance on a real space switch and picks the new default", async () => {
    const { router } = await setup({ instance: "on-1" });
    ctx.listInstances.mockResolvedValue({ items: [instance("hk-1")], page: { total: 1 } });
    ctx.space.selectedSpaceId = "hk";
    await nextTick();
    await flushPromises();
    expect(router.currentRoute.value.query).toEqual({ tab: "results", instance: "hk-1" });
    expect(ctx.store.loadInstanceDetail).toHaveBeenLastCalledWith("hk-1", 1, 20, false);
  });

  it("offers to create an instance when the space has none", async () => {
    ctx.listInstances.mockResolvedValue({ items: [], page: { total: 0 } });
    const { wrapper } = await setup({});
    expect(wrapper.find(".empty").text()).toContain("当前空间还没有策略实例");
    expect(wrapper.find(".empty").text()).toContain("新建实例");
  });

  it("does not write the URL once another tab is active", async () => {
    const { router } = await setup({ instance: "on-1" });
    await router.push({ path: "/strategy/tasks", query: { tab: "replay" } });
    await flushPromises();
    expect(router.currentRoute.value.query).toEqual({ tab: "replay" });
  });
});
