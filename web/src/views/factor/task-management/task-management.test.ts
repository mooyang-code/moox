import { flushPromises, mount } from "@vue/test-utils";
import { describe, expect, it, vi } from "vitest";
import { createMemoryHistory, createRouter } from "vue-router";

vi.mock("@/views/factor/compute-tasks/index.vue", () => ({ default: { template: '<div class="tab">tasks</div>' } }));
vi.mock("@/views/factor/results/index.vue", () => ({ default: { template: '<div class="tab">results</div>' } }));
vi.mock("@/views/factor/recalc/index.vue", () => ({ default: { template: '<div class="tab">recalc</div>' } }));

import FactorTaskManagement from "./index.vue";

async function setup(query: Record<string, string>) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: "/factor/tasks", name: "factor-tasks", component: FactorTaskManagement }]
  });
  await router.push({ path: "/factor/tasks", query });
  const wrapper = mount(FactorTaskManagement, { global: { plugins: [router] } });
  await flushPromises();
  return { router, wrapper };
}

describe("compute task host", () => {
  it("renders tabs in order 计算任务 -> 计算结果 -> 补算 without keep-alive", async () => {
    const { wrapper } = await setup({});
    const tabs = wrapper.findAll("button.page-title-tab");
    expect(tabs.map(tab => tab.text())).toEqual(["计算任务", "计算结果", "补算"]);
    expect(wrapper.find('[role="tablist"]').attributes("aria-label")).toBe("计算任务");
    expect(wrapper.find(".tab").text()).toBe("tasks");
  });

  it("derives the active tab from the URL and normalizes unknown values to tasks", async () => {
    expect((await setup({ tab: "results" })).wrapper.find(".tab").text()).toBe("results");
    expect((await setup({ tab: "recalc" })).wrapper.find(".tab").text()).toBe("recalc");
    expect((await setup({ tab: "unknown" })).wrapper.find(".tab").text()).toBe("tasks");
  });

  it("keeps ?task= and ?fc= when switching tabs and drops unrelated detail/job", async () => {
    const { router, wrapper } = await setup({ task: "ft_1", detail: "ft_1", fc: "x>1" });
    await wrapper
      .findAll("button.page-title-tab")
      .find(btn => btn.text() === "计算结果")!
      .trigger("click");
    await flushPromises();
    expect(router.currentRoute.value.query).toEqual({ tab: "results", task: "ft_1", fc: "x>1" });
    expect(wrapper.find(".tab").text()).toBe("results");

    await wrapper
      .findAll("button.page-title-tab")
      .find(btn => btn.text() === "补算")!
      .trigger("click");
    await flushPromises();
    expect(router.currentRoute.value.query).toEqual({ tab: "recalc", task: "ft_1", fc: "x>1" });
    expect(wrapper.find(".tab").text()).toBe("recalc");
  });
});
