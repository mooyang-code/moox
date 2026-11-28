import { flushPromises, mount } from "@vue/test-utils";
import { describe, expect, it } from "vitest";
import { defineComponent } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";
import { useQueryTab } from "./useQueryTab";

async function mountTabHost(initialQuery: Record<string, string>, options: Parameters<typeof useQueryTab>[0]) {
  let api!: ReturnType<typeof useQueryTab>;
  const Host = defineComponent({
    setup() {
      api = useQueryTab(options);
      return () => null;
    }
  });
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: "/demo", name: "demo", component: Host }]
  });
  await router.push({ path: "/demo", query: initialQuery });
  mount(Host, { global: { plugins: [router] } });
  await flushPromises();
  return { router, api };
}

describe("useQueryTab", () => {
  it("从 URL 派生当前页签，非法值回落到默认页签", async () => {
    const { api } = await mountTabHost({ tab: "unknown" }, { tabs: ["a", "b"] as const });
    expect(api.activeTab.value).toBe("a");
    expect(api.normalizeTab(["b"])).toBe("b");
    expect(api.normalizeTab("nope")).toBe("a");
  });

  it("切换页签默认省略默认页签参数并清掉其他 query", async () => {
    const { router, api } = await mountTabHost({ tab: "b", foo: "1" }, { tabs: ["a", "b"] as const, target: { name: "demo" } });
    expect(api.activeTab.value).toBe("b");
    api.setTab("a");
    await flushPromises();
    expect(router.currentRoute.value.query).toEqual({});
    expect(api.activeTab.value).toBe("a");

    api.setTab("b");
    await flushPromises();
    expect(router.currentRoute.value.query).toEqual({ tab: "b" });
    expect(api.activeTab.value).toBe("b");
  });

  it("支持自定义 buildQuery 保留指定参数", async () => {
    const { router, api } = await mountTabHost(
      { task: "t1", detail: "d1" },
      {
        tabs: ["tasks", "results"] as const,
        buildQuery: (tab, query) => ({
          ...(tab === "tasks" ? {} : { tab }),
          ...(typeof query.task === "string" ? { task: query.task } : {})
        })
      }
    );
    api.setTab("results");
    await flushPromises();
    expect(router.currentRoute.value.query).toEqual({ tab: "results", task: "t1" });
  });
});
