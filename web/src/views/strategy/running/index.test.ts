import { flushPromises, mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h, nextTick, reactive } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";

const api = vi.hoisted(() => ({
  loadInstances: vi.fn(),
  removeInstance: vi.fn(),
  clearInstances: vi.fn(),
  store: null as any,
  space: null as any,
  warning: vi.fn()
}));
vi.mock("@arco-design/web-vue", () => ({ Message: { success: vi.fn(), error: vi.fn() }, Modal: { warning: api.warning } }));
vi.mock("@/store/modules/space", () => ({ useSpaceStore: () => api.space }));
vi.mock("@/store/modules/strategy", () => ({ useStrategyStore: () => api.store }));

import RunningPage from "./index.vue";

const stubs = {
  "a-space": { template: "<div><slot /></div>" },
  "a-button": { template: "<button><slot /></button>" },
  "a-alert": { template: '<div class="alert"><slot /></div>' },
  "a-select": true,
  "a-option": true,
  "a-table": defineComponent({
    props: ["pagination"],
    emits: ["page-change"],
    setup(props, { emit }) {
      return () => h("button", { class: "goto-3", onClick: () => emit("page-change", 3) }, `page ${props.pagination.current}`);
    }
  }),
  "a-table-column": true,
  "a-empty": true,
  "a-dropdown": true,
  "a-doption": true,
  "icon-refresh": true,
  "icon-plus": true,
  StatusBadge: true,
  StrategyInstanceCreate: defineComponent({
    props: ["visible", "presetStrategyId"],
    setup(props) {
      return () => h("div", { class: "create" }, `create:${props.visible}:${props.presetStrategyId}`);
    }
  }),
  InstanceQuickDrawer: defineComponent({
    props: ["instanceId"],
    setup(props) {
      return () => h("div", { class: "quick" }, `quick:${props.instanceId}`);
    }
  })
};

async function setup(query: Record<string, string>) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/strategy/running", name: "strategy-running", component: RunningPage },
      { path: "/strategy/detail/:instanceId", name: "strategy-detail", component: { template: "<div />" } },
      { path: "/strategy/replay", name: "strategy-replay", component: { template: "<div />" } }
    ]
  });
  await router.push({ path: "/strategy/running", query });
  const wrapper = mount(RunningPage, { global: { plugins: [router], stubs } });
  await flushPromises();
  return { router, wrapper };
}

describe("strategy running page", () => {
  beforeEach(() => {
    api.loadInstances.mockReset().mockResolvedValue(undefined);
    api.removeInstance.mockReset().mockResolvedValue(undefined);
    api.clearInstances.mockReset();
    api.warning.mockReset();
    api.space = reactive({ selectedSpaceId: "crypto" });
    api.store = reactive({
      strategyCatalog: [] as unknown[],
      strategiesComplete: false,
      instances: [] as unknown[],
      totalInstances: 0,
      instancesLoading: false,
      instancesError: "",
      catalogError: "",
      loadInstances: api.loadInstances,
      removeInstance: api.removeInstance,
      clearInstances: api.clearInstances,
      loadAllStrategies: vi.fn().mockImplementation(async () => {
        api.store.strategyCatalog = [{ strategy_id: "s1", name: "动量" }];
        api.store.strategiesComplete = true;
      })
    });
  });

  it("loads the definition catalog once and skips it on later refreshes", async () => {
    const { wrapper } = await setup({});
    expect(api.store.loadAllStrategies).toHaveBeenCalledTimes(1);
    api.space.selectedSpaceId = "hk";
    await nextTick();
    await flushPromises();
    expect(api.loadInstances).toHaveBeenCalledTimes(2);
    expect(api.store.loadAllStrategies).toHaveBeenCalledTimes(1);
    wrapper.unmount();
  });

  it("restores filters, page and the quick detail from the URL with one request", async () => {
    const { wrapper } = await setup({ strategy_id: "s1", enabled: "false", page: "2", instance_id: "i1" });
    expect(api.loadInstances).toHaveBeenCalledTimes(1);
    expect(api.loadInstances).toHaveBeenCalledWith({ strategy_id: "s1", enabled: false, page: 2, page_size: 20 });
    expect(wrapper.find(".quick").text()).toBe("quick:i1");
    expect(wrapper.find(".create").text()).toBe("create:false:s1");
  });

  it("opens the create drawer from create=1 and drops the quick detail", async () => {
    const { wrapper } = await setup({ create: "1", instance_id: "i1", strategy_id: "s1" });
    expect(wrapper.find(".create").text()).toBe("create:true:s1");
    expect(wrapper.find(".quick").text()).toBe("quick:");
  });

  it("does not reload when only the overlay changes", async () => {
    const { router, wrapper } = await setup({ page: "2" });
    api.loadInstances.mockClear();
    await router.push({ path: "/strategy/running", query: { page: "2", instance_id: "i7" } });
    await flushPromises();
    expect(api.loadInstances).not.toHaveBeenCalled();
    expect(wrapper.find(".quick").text()).toBe("quick:i7");
  });

  it("writes paging with replace and reloads that page", async () => {
    const { router, wrapper } = await setup({ strategy_id: "s1" });
    await wrapper.find(".goto-3").trigger("click");
    await flushPromises();
    expect(router.currentRoute.value.query).toEqual({ page: "3", strategy_id: "s1" });
    expect(api.loadInstances).toHaveBeenLastCalledWith({ strategy_id: "s1", enabled: undefined, page: 3, page_size: 20 });
  });

  it("keeps the deep link when the space initializes but clears it on a real switch", async () => {
    api.space.selectedSpaceId = "";
    const { router } = await setup({ page: "2", instance_id: "i1" });
    expect(api.loadInstances).not.toHaveBeenCalled();
    api.space.selectedSpaceId = "crypto";
    await nextTick();
    await flushPromises();
    // 挂载时清理一次；空间初始化（无 → 有）不再清理。
    expect(api.clearInstances).toHaveBeenCalledTimes(1);
    expect(router.currentRoute.value.query).toEqual({ instance_id: "i1", page: "2" });
    expect(api.loadInstances).toHaveBeenCalledTimes(1);
    api.space.selectedSpaceId = "hk";
    await nextTick();
    await flushPromises();
    expect(api.clearInstances).toHaveBeenCalledTimes(2);
    expect(router.currentRoute.value.query).toEqual({});
    expect(api.loadInstances).toHaveBeenCalledTimes(2);
    expect(api.loadInstances).toHaveBeenLastCalledWith({ strategy_id: undefined, enabled: undefined, page: 1, page_size: 20 });
  });

  it("shows a catalog failure without marking the instance list as failed", async () => {
    api.store.catalogError = "目录失败";
    const { wrapper } = await setup({});
    const alerts = wrapper.findAll(".alert").map(item => item.text());
    expect(alerts.some(text => text.includes("策略定义目录加载失败"))).toBe(true);
    expect(alerts.some(text => text.includes("实例加载失败"))).toBe(false);
  });

  it("steps back a page when deleting empties the last page", async () => {
    const { router, wrapper } = await setup({ page: "3" });
    api.removeInstance.mockImplementation(async () => {
      api.store.instances = [];
    });
    api.store.instances = [{ instance_id: "i9", enabled: false, session_id: "" }];
    api.loadInstances.mockClear();
    const vm = wrapper.vm as unknown as { confirmRemove: (instance: unknown) => void };
    vm.confirmRemove({ instance_id: "i9", enabled: false, session_id: "" });
    await api.warning.mock.calls[0][0].onOk();
    await flushPromises();
    expect(router.currentRoute.value.query).toEqual({ page: "2" });
    expect(api.loadInstances).toHaveBeenCalledTimes(1);
    expect(api.loadInstances).toHaveBeenLastCalledWith({ strategy_id: undefined, enabled: undefined, page: 2, page_size: 20 });
  });
  it("closes the deleted instance's quick detail and steps back a page in one navigation", async () => {
    const { router, wrapper } = await setup({ page: "3", instance_id: "i9" });
    api.removeInstance.mockImplementation(async () => {
      api.store.instances = [];
    });
    api.store.instances = [{ instance_id: "i9", enabled: false, session_id: "" }];
    const vm = wrapper.vm as unknown as { confirmRemove: (instance: unknown) => void };
    vm.confirmRemove({ instance_id: "i9", enabled: false, session_id: "" });
    await api.warning.mock.calls.at(-1)[0].onOk();
    await flushPromises();
    expect(router.currentRoute.value.query).toEqual({ page: "2" });
  });

  it("lets only the latest of two quick space switches load the list", async () => {
    const { router } = await setup({ page: "2" });
    api.loadInstances.mockClear();
    api.space.selectedSpaceId = "hk";
    await nextTick();
    api.space.selectedSpaceId = "eu";
    await nextTick();
    await flushPromises();
    expect(router.currentRoute.value.query).toEqual({});
    expect(api.loadInstances).toHaveBeenCalledTimes(1);
    expect(api.loadInstances).toHaveBeenLastCalledWith({ strategy_id: undefined, enabled: undefined, page: 1, page_size: 20 });
  });

  it("clears instances left over from another space when the page mounts", async () => {
    await setup({});
    expect(api.clearInstances).toHaveBeenCalledTimes(1);
  });
});
