import { flushPromises, mount } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { createPinia, setActivePinia } from "pinia";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h } from "vue";
import { createMemoryHistory, createRouter, RouterView } from "vue-router";
import { setSelectedSpaceIdCache } from "@/api/admin/space-header";
import { routeViewKey } from "./route-view-key";

// 主题配置用了自动导入的 ref，测试里换成一个只有主视图要用的字段的 store
vi.mock("@/store/modules/theme-config", async () => {
  const { defineStore } = await import("pinia");
  const { ref } = await import("vue");
  return {
    useThemeConfig: defineStore("theme-config-test", () => ({
      refreshPage: ref(true)
    }))
  };
});

vi.mock("@/store/modules/space", async () => {
  const { defineStore } = await import("pinia");
  const { ref } = await import("vue");
  return {
    useSpaceStore: defineStore("space-test", () => {
      const selectedSpaceId = ref("crypto");
      return {
        selectedSpaceId,
        setSelectedSpace(id: string) {
          selectedSpaceId.value = id;
          setSelectedSpaceIdCache(id);
        }
      };
    })
  };
});

import { useSpaceStore } from "@/store/modules/space";

// 用真实路由与真实的 key 规则验证：query 变化时复用组件，换 path 或切空间内页面时按元信息重建。
describe("route view reuse with a real router", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    setSelectedSpaceIdCache("crypto");
  });

  function setup() {
    const counts = { reuse: 0, plain: 0, global: 0, spaceList: 0, spaceDetail: 0 };
    const seenQueries: Record<string, Record<string, unknown>[]> = { spaceList: [], spaceDetail: [] };
    const make = (name: keyof typeof counts, recordQuery = false) =>
      defineComponent({
        setup() {
          counts[name] += 1;
          return () => h("div", name);
        },
        created() {
          if (recordQuery && seenQueries[name]) {
            seenQueries[name].push({ ...this.$route.query });
          }
        }
      });
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: "/reuse", name: "reuse", component: make("reuse"), meta: { scope: "space" } },
        { path: "/plain", name: "plain", component: make("plain"), meta: { scope: "space" } },
        { path: "/ops/hosts", name: "ops-hosts", component: make("global"), meta: { scope: "global" } },
        { path: "/strategy/tasks", name: "strategy-tasks", component: make("spaceList", true), meta: { scope: "space" } },
        {
          path: "/strategy/detail/:id",
          name: "strategy-detail",
          component: make("spaceDetail", true),
          meta: { scope: "space", spaceRedirect: "/strategy/tasks" }
        }
      ]
    });
    const App = defineComponent({
      setup: () => () =>
        h(RouterView, null, { default: ({ Component, route }: any) => h(Component, { key: routeViewKey(route) }) })
    });
    return { counts, seenQueries, router, App };
  }

  async function mountMain(router: ReturnType<typeof setup>["router"]) {
    const { default: Main } = await import("./index.vue");
    return mount(Main, {
      global: { plugins: [router, ArcoVue], stubs: { Tabs: true, MainTransition: { template: "<div><slot /></div>" } } }
    });
  }

  it("keeps the instance across query changes", async () => {
    const { counts, router, App } = setup();
    await router.push("/reuse");
    mount(App, { global: { plugins: [router] } });
    await flushPromises();
    await router.push({ path: "/reuse", query: { page: "2" } });
    await router.replace({ path: "/reuse", query: { page: "2", instance_id: "i1" } });
    await flushPromises();
    expect(counts.reuse).toBe(1);
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
    const wrapper = await mountMain(router);
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

  it("切空间时全局页面不重建，保留当前实例与 URL 参数", async () => {
    const { counts, router } = setup();
    await router.push({ path: "/ops/hosts", query: { host: "h1" } });
    const wrapper = await mountMain(router);
    await flushPromises();
    expect(counts.global).toBe(1);

    useSpaceStore().setSelectedSpace("stockcn");
    await flushPromises();
    expect(counts.global).toBe(1);
    expect(router.currentRoute.value.fullPath).toBe("/ops/hosts?host=h1");
    wrapper.unmount();
  });

  it("切空间时空间内列表页保留 tab 页签并清空其余 URL 参数，再按新空间整页重建一次", async () => {
    const { counts, seenQueries, router } = setup();
    await router.push({ path: "/strategy/tasks", query: { tab: "replay", replay_id: "r1" } });
    const wrapper = await mountMain(router);
    await flushPromises();
    expect(counts.spaceList).toBe(1);
    expect(seenQueries.spaceList).toEqual([{ tab: "replay", replay_id: "r1" }]);

    useSpaceStore().setSelectedSpace("stockcn");
    await flushPromises();
    expect(router.currentRoute.value.path).toBe("/strategy/tasks");
    expect(router.currentRoute.value.query).toEqual({ tab: "replay" });
    expect(counts.spaceList).toBe(2);
    expect(seenQueries.spaceList).toEqual([{ tab: "replay", replay_id: "r1" }, { tab: "replay" }]);
    wrapper.unmount();
  });

  it("切空间时带对象 ID 的空间内详情页跳回列表且不重挂载旧详情", async () => {
    const { counts, router } = setup();
    await router.push("/strategy/detail/i1");
    const wrapper = await mountMain(router);
    await flushPromises();
    expect(counts.spaceDetail).toBe(1);
    expect(counts.spaceList).toBe(0);

    useSpaceStore().setSelectedSpace("stockcn");
    await flushPromises();
    expect(router.currentRoute.value.path).toBe("/strategy/tasks");
    expect(counts.spaceDetail).toBe(1);
    expect(counts.spaceList).toBe(1);
    wrapper.unmount();
  });

  it("空间首次初始化（无 → 有）保留深链接参数并按新代次重挂载，不跳回列表", async () => {
    const { counts, seenQueries, router } = setup();
    const spaceStore = useSpaceStore();
    spaceStore.selectedSpaceId = "";
    setSelectedSpaceIdCache("");
    await router.push({ path: "/strategy/tasks", query: { page: "2", create: "1" } });
    const wrapper = await mountMain(router);
    await flushPromises();
    expect(counts.spaceList).toBe(1);

    spaceStore.setSelectedSpace("crypto");
    await flushPromises();
    expect(router.currentRoute.value.query).toEqual({ page: "2", create: "1" });
    expect(counts.spaceList).toBe(2);
    expect(seenQueries.spaceList).toEqual([
      { page: "2", create: "1" },
      { page: "2", create: "1" }
    ]);
    wrapper.unmount();
  });

  it("程序化跨空间跳转（全局页切空间后立刻 router.push）不会误清目标页参数", async () => {
    const { counts, seenQueries, router } = setup();
    await router.push("/ops/hosts");
    const wrapper = await mountMain(router);
    await flushPromises();

    useSpaceStore().setSelectedSpace("stockcn");
    await router.push({ path: "/strategy/tasks", query: { tab: "replay", replay_id: "r2" } });
    await flushPromises();
    expect(router.currentRoute.value.query).toEqual({ tab: "replay", replay_id: "r2" });
    expect(counts.spaceList).toBe(1);
    expect(seenQueries.spaceList).toEqual([{ tab: "replay", replay_id: "r2" }]);
    wrapper.unmount();
  });
});
