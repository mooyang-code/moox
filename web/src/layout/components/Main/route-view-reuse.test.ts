import { flushPromises, mount } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { describe, expect, it, vi } from "vitest";

// 主题配置用了自动导入的 ref，测试里换成一个只有主视图要用的字段的 store
vi.mock("@/store/modules/theme-config", async () => {
  const { defineStore } = await import("pinia");
  const { ref } = await import("vue");
  return {
    useThemeConfig: defineStore("theme-config-test", () => ({
      refreshPage: ref(true),
      isTabs: ref(false)
    }))
  };
});
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

  it("跨路径导航时旧页面停留到新页面就绪，不被替换成“页面切换中”", async () => {
    const { router } = setup();
    let release: () => void = () => undefined;
    router.beforeEach(to => (to.path === "/plain" ? new Promise<void>(resolve => (release = resolve)) : undefined));
    await router.push("/reuse");
    const { default: Main } = await import("./index.vue");
    const wrapper = mount(Main, {
      global: { plugins: [router, ArcoVue], stubs: { Tabs: true, MainTransition: { template: "<div><slot /></div>" } } }
    });
    await flushPromises();
    expect(wrapper.text()).toContain("reuse");
    const navigation = router.push("/plain");
    await flushPromises();
    // 守卫还没放行：旧页面还在，没有加载提示
    expect(wrapper.text()).toContain("reuse");
    expect(wrapper.text()).not.toContain("页面切换中");
    release();
    await navigation;
    await flushPromises();
    expect(wrapper.text()).toContain("plain");
  });
});
