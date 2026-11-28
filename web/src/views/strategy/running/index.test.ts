import { flushPromises, mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h, nextTick, reactive } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";

const api = vi.hoisted(() => ({
  loadInstances: vi.fn(),
  clearInstances: vi.fn(),
  listLogicalAccounts: vi.fn(),
  getCollectorStatus: vi.fn(),
  store: null as any,
  space: null as any,
  warning: vi.fn(),
  confirmToggle: vi.fn(),
  enableOpen: vi.fn()
}));
vi.mock("@arco-design/web-vue", () => ({ Message: { success: vi.fn(), error: vi.fn() }, Modal: { warning: api.warning } }));
vi.mock("@/api/trade", () => ({ listLogicalAccounts: api.listLogicalAccounts }));
vi.mock("@/api/collector/runtime-status", () => ({ getCollectorStatus: api.getCollectorStatus }));
vi.mock("@/views/strategy/components/instance-toggle", () => ({ confirmInstanceToggle: api.confirmToggle }));
vi.mock("@/store/modules/space", () => ({ useSpaceStore: () => api.space }));
vi.mock("@/store/modules/strategy", () => ({ useStrategyStore: () => api.store }));
// 实例列表与定义目录在测试里共用同一个假对象
vi.mock("@/views/strategy/use-instance-list", () => ({ useInstanceList: () => api.store }));

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
  FactorInfoTip: true,
  StatusBadge: true,
  StrategyInstanceCreate: defineComponent({
    props: ["visible", "presetStrategyId"],
    emits: ["created"],
    setup(props, { emit }) {
      return () =>
        h("div", { class: "create" }, [
          `create:${props.visible}:${props.presetStrategyId}`,
          h("button", { class: "emit-created", onClick: () => emit("created", "i-new") })
        ]);
    }
  }),
  InstanceEnableDialog: { template: "<div />", methods: { open: api.enableOpen } },
  InstanceConfirmDialog: { template: "<div />", methods: { open: vi.fn() } }
};

async function setup(query: Record<string, string>) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/strategy/tasks", name: "strategy-tasks", component: RunningPage },
      { path: "/strategy/detail/:instanceId", name: "strategy-detail", component: { template: "<div />" } }
    ]
  });
  await router.push({ path: "/strategy/tasks", query });
  const wrapper = mount(RunningPage, { global: { plugins: [router], stubs } });
  await flushPromises();
  return { router, wrapper };
}

describe("strategy running page", () => {
  beforeEach(() => {
    api.loadInstances.mockReset().mockResolvedValue(undefined);
    api.clearInstances.mockReset();
    api.listLogicalAccounts.mockReset().mockResolvedValue({ logical_accounts: [] });
    api.getCollectorStatus.mockReset().mockResolvedValue({ history_seeds: [] });
    api.warning.mockReset();
    api.confirmToggle.mockReset();
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

  it("restores filters and page from the URL with one request", async () => {
    const { wrapper } = await setup({ strategy_id: "s1", enabled: "false", page: "2" });
    expect(api.loadInstances).toHaveBeenCalledTimes(1);
    expect(api.loadInstances).toHaveBeenCalledWith(
      { strategy_id: "s1", enabled: false, page: 2, size: 20 },
      { background: false }
    );
    expect(wrapper.find(".create").text()).toBe("create:false:s1");
  });

  it("opens the create drawer from create=1", async () => {
    const { wrapper } = await setup({ create: "1", strategy_id: "s1" });
    expect(wrapper.find(".create").text()).toBe("create:true:s1");
  });

  it("ends on the detail page of a newly created instance instead of falling back to the list", async () => {
    const { router, wrapper } = await setup({ create: "1", strategy_id: "s1" });
    await wrapper.find(".emit-created").trigger("click");
    await flushPromises();
    expect(router.currentRoute.value.name).toBe("strategy-detail");
    expect(router.currentRoute.value.params.instanceId).toBe("i-new");
    // 后退回到列表，不再带 create=1（不会重新弹出空的创建抽屉）
    router.back();
    await new Promise(resolve => setTimeout(resolve, 20));
    await flushPromises();
    expect(router.currentRoute.value.query.create).toBeUndefined();
    expect(router.currentRoute.value.query.strategy_id).toBe("s1");
  });

  it("does not reload when only the create drawer toggles", async () => {
    const { router, wrapper } = await setup({ page: "2" });
    api.loadInstances.mockClear();
    await router.push({ path: "/strategy/tasks", query: { page: "2", create: "1" } });
    await flushPromises();
    expect(api.loadInstances).not.toHaveBeenCalled();
    expect(wrapper.find(".create").text()).toBe("create:true:");
  });

  it("writes paging with replace and reloads that page", async () => {
    const { router, wrapper } = await setup({ strategy_id: "s1" });
    await wrapper.find(".goto-3").trigger("click");
    await flushPromises();
    expect(router.currentRoute.value.query).toEqual({ page: "3", strategy_id: "s1" });
    expect(api.loadInstances).toHaveBeenLastCalledWith(
      { strategy_id: "s1", enabled: undefined, page: 3, size: 20 },
      { background: false }
    );
  });

  it("keeps the deep link when the space initializes but clears it on a real switch", async () => {
    api.space.selectedSpaceId = "";
    const { router } = await setup({ page: "2", create: "1" });
    expect(api.loadInstances).not.toHaveBeenCalled();
    api.space.selectedSpaceId = "crypto";
    await nextTick();
    await flushPromises();
    // 挂载时清理一次；空间初始化（无 → 有）不再清理。
    expect(api.clearInstances).toHaveBeenCalledTimes(1);
    expect(router.currentRoute.value.query).toEqual({ create: "1", page: "2" });
    expect(api.loadInstances).toHaveBeenCalledTimes(1);
    api.space.selectedSpaceId = "hk";
    await nextTick();
    await flushPromises();
    expect(api.clearInstances).toHaveBeenCalledTimes(2);
    expect(router.currentRoute.value.query).toEqual({});
    expect(api.loadInstances).toHaveBeenCalledTimes(2);
    expect(api.loadInstances).toHaveBeenLastCalledWith(
      { strategy_id: undefined, enabled: undefined, page: 1, size: 20 },
      { background: false }
    );
  });

  it("shows a catalog failure without marking the instance list as failed", async () => {
    api.store.catalogError = "目录失败";
    const { wrapper } = await setup({});
    const alerts = wrapper.findAll(".alert").map(item => item.text());
    expect(alerts.some(text => text.includes("策略定义目录加载失败"))).toBe(true);
    expect(alerts.some(text => text.includes("实例加载失败"))).toBe(false);
  });

  it("toggles an instance from the list through the shared confirm flow and reloads afterwards", async () => {
    const { wrapper } = await setup({ page: "2" });
    api.loadInstances.mockClear();
    const vm = wrapper.vm as unknown as { toggle: (instance: unknown) => void; togglingId: string };
    vm.toggle({ instance_id: "i5", enabled: true, session_id: "s" });
    expect(api.confirmToggle).toHaveBeenCalledWith("i5", false, expect.any(Object));
    const hooks = api.confirmToggle.mock.calls[0][2];
    hooks.onStart();
    expect(vm.togglingId).toBe("i5");
    await hooks.onSettled(false);
    expect(api.loadInstances).toHaveBeenCalledTimes(1);
    expect(api.loadInstances).toHaveBeenLastCalledWith(
      { strategy_id: undefined, enabled: undefined, page: 2, size: 20 },
      { background: false }
    );
    expect(vm.togglingId).toBe("");
  });

  it("enables a trade-mode instance through the dialog that names the account and environment", async () => {
    const { wrapper } = await setup({});
    api.confirmToggle.mockClear();
    api.enableOpen.mockClear();
    const vm = wrapper.vm as unknown as { toggle: (instance: unknown) => void };
    const instance = { instance_id: "i6", enabled: false, session_id: "", logical_account_id: "la-1", factor_tasks: {} };
    vm.toggle(instance);
    expect(api.enableOpen).toHaveBeenCalledWith(instance, expect.any(Object));
    expect(api.confirmToggle).not.toHaveBeenCalled();
    // 停用、仅计算的实例仍走通用确认
    vm.toggle({ ...instance, enabled: true });
    vm.toggle({ ...instance, instance_id: "i7", logical_account_id: "" });
    expect(api.confirmToggle).toHaveBeenCalledTimes(2);
    expect(api.enableOpen).toHaveBeenCalledTimes(1);
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
    expect(api.loadInstances).toHaveBeenLastCalledWith(
      { strategy_id: undefined, enabled: undefined, page: 1, size: 20 },
      { background: false }
    );
  });

  it("clears instances left over from another space when the page mounts", async () => {
    await setup({});
    expect(api.clearInstances).toHaveBeenCalledTimes(1);
  });

  it("refreshes logicalNames when switching spaces and drops stale responses", async () => {
    let resolveCrypto!: (value: unknown) => void;
    api.listLogicalAccounts
      .mockImplementationOnce(
        () =>
          new Promise(resolve => {
            resolveCrypto = resolve;
          })
      )
      .mockResolvedValueOnce({ logical_accounts: [{ logical_account_id: "la-hk", name: "港股组合" }] });

    const { wrapper } = await setup({});
    const vm = wrapper.vm as unknown as { logicalNames: Record<string, string> };
    expect(api.listLogicalAccounts).toHaveBeenCalledTimes(1);
    expect(vm.logicalNames).toEqual({});

    // 切空间时立刻发起新空间的组合名读取，旧空间迟到的响应不能覆盖新空间结果
    api.space.selectedSpaceId = "hk";
    await nextTick();
    await flushPromises();
    expect(api.listLogicalAccounts).toHaveBeenCalledTimes(2);
    expect(vm.logicalNames).toEqual({ "la-hk": "港股组合" });

    resolveCrypto({ logical_accounts: [{ logical_account_id: "la-crypto", name: "币圈组合" }] });
    await flushPromises();
    expect(vm.logicalNames).toEqual({ "la-hk": "港股组合" });

    // 切到下一个空间但加载失败时，应清空旧空间名称而不残留
    api.listLogicalAccounts.mockRejectedValueOnce(new Error("trade unavailable"));
    api.space.selectedSpaceId = "us";
    await nextTick();
    await flushPromises();
    expect(api.listLogicalAccounts).toHaveBeenCalledTimes(3);
    expect(vm.logicalNames).toEqual({});
    wrapper.unmount();
  });

  it("waits for space initialization before loading logicalNames", async () => {
    api.space.selectedSpaceId = "";
    api.listLogicalAccounts.mockResolvedValueOnce({ logical_accounts: [{ logical_account_id: "la-1", name: "实盘一号" }] });
    const { wrapper } = await setup({});
    const vm = wrapper.vm as unknown as { logicalNames: Record<string, string> };
    expect(api.listLogicalAccounts).not.toHaveBeenCalled();

    api.space.selectedSpaceId = "crypto";
    await nextTick();
    await flushPromises();
    expect(api.listLogicalAccounts).toHaveBeenCalledTimes(1);
    expect(vm.logicalNames).toEqual({ "la-1": "实盘一号" });
    wrapper.unmount();
  });

  it("clears instances, logicalNames and invalidates in-flight space switch when selectedSpaceId becomes empty", async () => {
    api.listLogicalAccounts.mockResolvedValue({ logical_accounts: [{ logical_account_id: "la-1", name: "币圈组合" }] });
    const { router, wrapper } = await setup({ page: "2" });
    const vm = wrapper.vm as unknown as { logicalNames: Record<string, string> };
    expect(vm.logicalNames).toEqual({ "la-1": "币圈组合" });

    api.loadInstances.mockClear();
    api.listLogicalAccounts.mockClear();
    api.clearInstances.mockClear();

    // 模拟切空间触发的路由 replace 挂起期间，空间被清空为 ""
    let finishReplace!: () => void;
    const originalReplace = router.replace.bind(router);
    vi.spyOn(router, "replace").mockImplementationOnce(
      () =>
        new Promise(resolve => {
          finishReplace = () => resolve(originalReplace({ name: "strategy-tasks", query: {} }));
        }) as ReturnType<typeof router.replace>
    );

    api.space.selectedSpaceId = "hk";
    await nextTick();
    api.space.selectedSpaceId = "";
    await nextTick();
    expect(api.clearInstances).toHaveBeenCalledTimes(2);
    expect(vm.logicalNames).toEqual({});

    finishReplace();
    await flushPromises();
    // 原先挂起的 hk 切换因 spaceGeneration 递增而退出，不再发任何读取请求
    expect(api.loadInstances).not.toHaveBeenCalled();
    expect(api.listLogicalAccounts).not.toHaveBeenCalled();
    wrapper.unmount();
  });
});
