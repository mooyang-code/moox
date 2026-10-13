import { flushPromises, mount } from "@vue/test-utils";
import { describe, expect, it, vi } from "vitest";
import { createMemoryHistory, createRouter } from "vue-router";

// 三个 Tab 换成只显示名字的替身：这里只验证工作台按 URL 切换与清理 query。
vi.mock("@/views/strategy/running/index.vue", () => ({ default: { template: '<div class="tab">instances</div>' } }));
vi.mock("@/views/strategy/results/index.vue", () => ({ default: { template: '<div class="tab">results</div>' } }));
vi.mock("@/views/strategy/replay/index.vue", () => ({ default: { template: '<div class="tab">replay</div>' } }));

import Workbench from "./index.vue";

async function setup(query: Record<string, string>) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: "/strategy/tasks", name: "strategy-tasks", component: Workbench, meta: { reuseOnQuery: true } }]
  });
  await router.push({ path: "/strategy/tasks", query });
  const wrapper = mount(Workbench, { global: { plugins: [router] } });
  await flushPromises();
  return { router, wrapper };
}

describe("strategy workbench", () => {
  it("shows the tab named in the URL and defaults to the instances tab", async () => {
    expect((await setup({})).wrapper.find(".tab").text()).toBe("instances");
    expect((await setup({ tab: "results", instance: "i1" })).wrapper.find(".tab").text()).toBe("results");
    expect((await setup({ tab: "nope" })).wrapper.find(".tab").text()).toBe("instances");
  });

  it("switching tabs drops the previous tab's query", async () => {
    const { router, wrapper } = await setup({ tab: "results", instance: "i1" });
    const buttons = wrapper.findAll("button.page-title-tab");
    await buttons.find(item => item.text() === "策略回放")!.trigger("click");
    await flushPromises();
    expect(router.currentRoute.value.query).toEqual({ tab: "replay" });
    expect(wrapper.find(".tab").text()).toBe("replay");
    await buttons.find(item => item.text() === "运行实例")!.trigger("click");
    await flushPromises();
    expect(router.currentRoute.value.query).toEqual({});
  });
});
