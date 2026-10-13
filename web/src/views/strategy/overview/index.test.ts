import { flushPromises, mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h, reactive } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";

const api = vi.hoisted(() => ({ loadStrategies: vi.fn(), space: { selectedSpaceId: "space-1" } }));
vi.mock("@/store/modules/space", () => ({ useSpaceStore: () => api.space }));
vi.mock("@/store/modules/strategy", () => ({
  useStrategyStore: () =>
    reactive({
      strategies: [],
      totalStrategies: 0,
      listLoading: false,
      error: "",
      loadStrategies: api.loadStrategies,
      removeStrategy: vi.fn()
    })
}));
vi.mock("@arco-design/web-vue", () => ({ Message: { success: vi.fn(), error: vi.fn() }, Modal: { warning: vi.fn() } }));

import OverviewPage from "./index.vue";

const stubs = {
  "a-table": defineComponent({
    props: ["pagination"],
    emits: ["page-change"],
    setup(props, { emit }) {
      return () => h("button", { class: "goto-3", onClick: () => emit("page-change", 3) }, `page ${props.pagination.current}`);
    }
  }),
  StrategyDefinitionDrawer: defineComponent({
    props: ["definitionId"],
    emits: ["close"],
    setup(props, { emit }) {
      return () => h("button", { class: "drawer", onClick: () => emit("close") }, `drawer:${props.definitionId}`);
    }
  }),
  "a-alert": true,
  "a-space": { template: "<div><slot /></div>" },
  "a-button": { template: "<button><slot /></button>" },
  "icon-refresh": true,
  "icon-plus": true
};

async function setup(query: Record<string, string>) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/strategy/overview", name: "strategy-overview", component: OverviewPage, meta: { reuseOnQuery: true } },
      { path: "/strategy/running", name: "strategy-running", component: { template: "<div />" } }
    ]
  });
  await router.push({ path: "/strategy/overview", query });
  const wrapper = mount(OverviewPage, { global: { plugins: [router], stubs } });
  await flushPromises();
  return { router, wrapper };
}

describe("strategy definitions page URL state", () => {
  beforeEach(() => {
    api.loadStrategies.mockReset().mockResolvedValue(undefined);
    api.space.selectedSpaceId = "space-1";
  });

  it("restores page and detail drawer from the URL", async () => {
    const { wrapper } = await setup({ page: "2", definition_id: "s1" });
    expect(api.loadStrategies).toHaveBeenCalledWith({ page: 2, page_size: 20 });
    expect(wrapper.find(".drawer").text()).toBe("drawer:s1");
  });

  it("normalizes an invalid page and keeps the deep link when the space initializes", async () => {
    api.space.selectedSpaceId = "";
    const { wrapper } = await setup({ page: "abc", definition_id: "s9" });
    expect(api.loadStrategies).not.toHaveBeenCalled();
    expect(wrapper.find(".drawer").text()).toBe("drawer:s9");
  });

  it("writes paging with replace and closing the drawer with replace, keeping the page", async () => {
    const { router, wrapper } = await setup({ definition_id: "s1" });
    const replace = vi.spyOn(router, "replace");
    await wrapper.find(".goto-3").trigger("click");
    await flushPromises();
    expect(replace).toHaveBeenLastCalledWith({ name: "strategy-overview", query: { definition_id: "s1", page: "3" } });
    expect(api.loadStrategies).toHaveBeenLastCalledWith({ page: 3, page_size: 20 });
    await wrapper.find(".drawer").trigger("click");
    await flushPromises();
    expect(router.currentRoute.value.query).toEqual({ page: "3" });
    expect(wrapper.find(".drawer").text()).toBe("drawer:");
  });

  it("reloads once for a back navigation to another page", async () => {
    const { router } = await setup({ page: "2" });
    api.loadStrategies.mockClear();
    await router.push({ path: "/strategy/overview", query: { page: "4" } });
    await flushPromises();
    expect(api.loadStrategies).toHaveBeenCalledTimes(1);
    expect(api.loadStrategies).toHaveBeenCalledWith({ page: 4, page_size: 20 });
  });

  it("does not reload when only the drawer id changes", async () => {
    const { router, wrapper } = await setup({ page: "2" });
    api.loadStrategies.mockClear();
    await router.push({ path: "/strategy/overview", query: { page: "2", definition_id: "s7" } });
    await flushPromises();
    expect(api.loadStrategies).not.toHaveBeenCalled();
    expect(wrapper.find(".drawer").text()).toBe("drawer:s7");
  });

  it("does not react to query changes after leaving the page", async () => {
    const { router } = await setup({ page: "2" });
    api.loadStrategies.mockClear();
    await router.push({ name: "strategy-running", query: { page: "9" } });
    await flushPromises();
    expect(api.loadStrategies).not.toHaveBeenCalled();
  });
});
