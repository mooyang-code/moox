import { flushPromises, mount } from "@vue/test-utils";
import { describe, expect, it, vi } from "vitest";
import type { VueWrapper } from "@vue/test-utils";
import { createMemoryHistory, createRouter } from "vue-router";

// 三个 Tab 换成只显示名字的替身：这里只验证工作台按 URL 切换与清理 query。
vi.mock("@/views/strategy/running/index.vue", () => ({ default: { template: '<div class="tab">instances</div>' } }));
vi.mock("@/views/strategy/results/index.vue", () => ({ default: { template: '<div class="tab">results</div>' } }));
vi.mock("@/views/strategy/replay/index.vue", () => ({ default: { template: '<div class="tab">replay</div>' } }));

import Workbench from "./index.vue";

/** Tab 是异步组件：等到它渲染出指定内容。 */
async function tabText(wrapper: VueWrapper, expected: string) {
  await vi.waitFor(async () => {
    await flushPromises();
    expect(wrapper.find(".tab").exists() && wrapper.find(".tab").text()).toBe(expected);
  });
}

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
    await tabText((await setup({})).wrapper, "instances");
    await tabText((await setup({ tab: "results", instance: "i1" })).wrapper, "results");
    await tabText((await setup({ tab: "nope" })).wrapper, "instances");
  });

  it("switching tabs drops the previous tab's query", async () => {
    const { router, wrapper } = await setup({ tab: "results", instance: "i1" });
    const buttons = wrapper.findAll("button.page-title-tab");
    await buttons.find(item => item.text() === "策略回放")!.trigger("click");
    await flushPromises();
    expect(router.currentRoute.value.query).toEqual({ tab: "replay" });
    await tabText(wrapper, "replay");
    await buttons.find(item => item.text() === "运行实例")!.trigger("click");
    await flushPromises();
    expect(router.currentRoute.value.query).toEqual({});
  });
});
