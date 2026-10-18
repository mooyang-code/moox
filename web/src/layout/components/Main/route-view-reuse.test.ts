import { flushPromises, mount } from "@vue/test-utils";
import { describe, expect, it } from "vitest";
import { defineComponent, h } from "vue";
import { createMemoryHistory, createRouter, RouterView } from "vue-router";
import { routeViewKey } from "./route-view-key";

// 用真实路由与真实的 key 规则验证：reuseOnQuery 页面在 query 变化时复用组件，其余页面按完整路径重建。
describe("route view reuse with a real router", () => {
  function setup() {
    const counts = { reuse: 0, plain: 0 };
    const make = (name: keyof typeof counts) =>
      defineComponent({
        setup() {
          counts[name] += 1;
          return () => h("div", name);
        }
      });
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: "/reuse", name: "reuse", component: make("reuse"), meta: { reuseOnQuery: true } },
        { path: "/plain", name: "plain", component: make("plain") }
      ]
    });
    const App = defineComponent({
      setup: () => () =>
        h(RouterView, null, { default: ({ Component, route }: any) => h(Component, { key: routeViewKey(route) }) })
    });
    return { counts, router, App };
  }

  it("keeps the instance across query changes on reuseOnQuery routes", async () => {
    const { counts, router, App } = setup();
    await router.push("/reuse");
    mount(App, { global: { plugins: [router] } });
    await flushPromises();
    await router.push({ path: "/reuse", query: { page: "2" } });
    await router.replace({ path: "/reuse", query: { page: "2", instance_id: "i1" } });
    await flushPromises();
    expect(counts.reuse).toBe(1);
  });

  it("rebuilds other routes when the query changes", async () => {
    const { counts, router, App } = setup();
    await router.push("/plain");
    mount(App, { global: { plugins: [router] } });
    await flushPromises();
    await router.push({ path: "/plain", query: { tab: "b" } });
    await flushPromises();
    expect(counts.plain).toBe(2);
  });

  it("unmounts when leaving for another path", async () => {
    const { counts, router, App } = setup();
    await router.push("/reuse");
    mount(App, { global: { plugins: [router] } });
    await flushPromises();
    await router.push("/plain");
    await router.push("/reuse");
    await flushPromises();
    expect(counts.reuse).toBe(2);
  });
});
