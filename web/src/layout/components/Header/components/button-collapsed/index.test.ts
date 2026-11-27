import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import piniaPluginPersistedstate from "pinia-plugin-persistedstate";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { computed, createApp, defineComponent, h, nextTick, ref } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";
import { useThemeConfig } from "@/store/modules/theme-config";

const viewport = vi.hoisted(() => ({ isPc: null as ReturnType<typeof ref<boolean>> | null }));
vi.mock("@/hooks/useDevicesSize", () => ({
  useDevicesSize: () => ({
    isPc: computed(() => viewport.isPc!.value),
    isPad: computed(() => false),
    isMobile: computed(() => !viewport.isPc!.value)
  })
}));

import ButtonCollapsed from "./index.vue";
import Aside from "@/layout/components/Aside/index.vue";
import DrawerAside from "@/layout/components/drawer-aside/index.vue";

const stubs = {
  DrawerAside: { template: '<div class="drawer-stub" />' },
  "a-button": { template: '<button class="fold-btn"><slot name="icon" /></button>' },
  "icon-menu-fold": { template: '<span class="icon-fold" />' },
  "icon-menu-unfold": { template: '<span class="icon-unfold" />' }
};

function createPersistedPinia() {
  const pinia = createPinia();
  pinia.use(piniaPluginPersistedstate);
  // Pinia 只有在 app.use(pinia) 安装后才会激活已注册插件（如 pinia-plugin-persistedstate）
  createApp({}).use(pinia);
  setActivePinia(pinia);
  return pinia;
}

describe("侧栏折叠按钮与移动端抽屉隔离", () => {
  beforeEach(() => {
    localStorage.clear();
    createPersistedPinia();
    viewport.isPc = ref(true);
  });

  afterEach(() => {
    localStorage.clear();
  });

  it("桌面端挂载时不覆盖持久化保存的折叠状态", () => {
    localStorage.setItem("theme-config", JSON.stringify({ collapsed: true, darkMode: false }));
    createPersistedPinia();
    const store = useThemeConfig();

    const wrapper = mount(ButtonCollapsed, { global: { stubs } });
    expect(store.collapsed).toBe(true);
    expect(wrapper.find(".icon-unfold").exists()).toBe(true);
    expect(wrapper.find(".drawer-stub").exists()).toBe(false);
    wrapper.unmount();
  });

  it("桌面端默认展开状态在刷新后保持展开", () => {
    const store = useThemeConfig();
    store.setCollapsed(false);

    const wrapper = mount(ButtonCollapsed, { global: { stubs } });
    expect(store.collapsed).toBe(false);
    expect(wrapper.find(".icon-fold").exists()).toBe(true);
    wrapper.unmount();
  });

  it("窄屏刷新与开关抽屉只操作非持久化 drawerVisible，绝不污染桌面端 localStorage.collapsed", async () => {
    localStorage.setItem("theme-config", JSON.stringify({ collapsed: false, darkMode: false }));
    createPersistedPinia();
    const store = useThemeConfig();
    viewport.isPc!.value = false;

    const wrapper = mount(ButtonCollapsed, { global: { stubs } });
    // 移动端初始抽屉收起，按钮显示展开图标，但桌面端 collapsed 与 localStorage 保持 false 不变
    expect(store.drawerVisible).toBe(false);
    expect(store.collapsed).toBe(false);
    expect(wrapper.find(".icon-unfold").exists()).toBe(true);
    expect(wrapper.find(".drawer-stub").exists()).toBe(true);
    expect(JSON.parse(localStorage.getItem("theme-config") || "{}")).toEqual({ collapsed: false, darkMode: false });

    // 移动端点击按钮展开/收起抽屉，仍不改写 localStorage 中的 collapsed 且不写入 drawerVisible
    await wrapper.find(".fold-btn").trigger("click");
    expect(store.drawerVisible).toBe(true);
    expect(wrapper.find(".icon-fold").exists()).toBe(true);
    expect(store.collapsed).toBe(false);
    const storedAfterOpen = JSON.parse(localStorage.getItem("theme-config") || "{}");
    expect(storedAfterOpen).toEqual({ collapsed: false, darkMode: false });
    expect("drawerVisible" in storedAfterOpen).toBe(false);

    await wrapper.find(".fold-btn").trigger("click");
    expect(store.drawerVisible).toBe(false);
    expect(store.collapsed).toBe(false);
    expect(JSON.parse(localStorage.getItem("theme-config") || "{}")).toEqual({ collapsed: false, darkMode: false });

    // 切回桌面端点击折叠会真正写入 localStorage，随后再切到窄屏开关抽屉也不污染已持久化的 true
    viewport.isPc!.value = true;
    await nextTick();
    await wrapper.find(".fold-btn").trigger("click");
    expect(store.collapsed).toBe(true);
    expect(JSON.parse(localStorage.getItem("theme-config") || "{}")).toEqual({ collapsed: true, darkMode: false });

    viewport.isPc!.value = false;
    await nextTick();
    await wrapper.find(".fold-btn").trigger("click");
    expect(store.drawerVisible).toBe(true);
    expect(store.collapsed).toBe(true);
    expect(JSON.parse(localStorage.getItem("theme-config") || "{}")).toEqual({ collapsed: true, darkMode: false });
    wrapper.unmount();
  });

  it("跨断点切换仅将 drawerVisible 复位为 false，不改写桌面端折叠偏好", async () => {
    const store = useThemeConfig();
    store.setCollapsed(false);

    const wrapper = mount(ButtonCollapsed, { global: { stubs } });
    // 桌面端点击折叠，写入持久化偏好
    await wrapper.find(".fold-btn").trigger("click");
    expect(store.collapsed).toBe(true);

    // 缩窄到移动端并展开抽屉
    viewport.isPc!.value = false;
    await nextTick();
    expect(store.drawerVisible).toBe(false);
    expect(store.collapsed).toBe(true);
    await wrapper.find(".fold-btn").trigger("click");
    expect(store.drawerVisible).toBe(true);
    expect(store.collapsed).toBe(true);

    // 拉宽回桌面端：仅抽屉关闭，桌面端 collapsed 依然为 true
    viewport.isPc!.value = true;
    await nextTick();
    expect(store.drawerVisible).toBe(false);
    expect(store.collapsed).toBe(true);
    wrapper.unmount();
  });

  it("桌面端处于折叠态（collapsed=true）时，打开移动端抽屉内部 Logo 与 Menu 仍固定以展开态渲染", async () => {
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: "/home", name: "home", component: { template: "<div />" } }]
    });
    await router.push("/home");

    const store = useThemeConfig();
    store.setCollapsed(true);
    store.setDrawerVisible(true);

    let receivedMenuCollapsed: boolean | undefined;
    let receivedMenuBreakpoint: string | undefined;
    const wrapper = mount(DrawerAside, {
      global: {
        plugins: [router],
        stubs: {
          "a-drawer": { props: ["visible"], template: '<div class="drawer-body" v-if="visible"><slot /></div>' },
          "a-layout-sider": { template: "<div><slot /></div>" },
          "a-scrollbar": { template: "<div><slot /></div>" },
          SvgIcon: { template: '<i class="svg-logo" />' },
          MenuItem: { template: "<div />" },
          "a-menu": defineComponent({
            props: ["collapsed", "breakpoint"],
            setup(props, { slots }) {
              return () => {
                receivedMenuCollapsed = props.collapsed;
                receivedMenuBreakpoint = props.breakpoint;
                return h("div", { class: "menu-stub", "data-collapsed": String(props.collapsed) }, slots.default?.());
              };
            }
          })
        }
      }
    });

    // Logo 不带折叠类且渲染标题文字，Menu 强制 collapsed=false 且不绑定响应式 breakpoint
    expect(wrapper.find(".logo_box").classes()).not.toContain("padding-unset");
    expect(wrapper.find(".logo_title").exists()).toBe(true);
    expect(receivedMenuCollapsed).toBe(false);
    expect(receivedMenuBreakpoint).toBeUndefined();
    expect(store.collapsed).toBe(true);
    wrapper.unmount();
  });

  it("桌面端 Aside 省略 collapsed prop 时，Logo 与 Menu 正确跟随 store.collapsed 折叠并保留 xl breakpoint", async () => {
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: "/home", name: "home", component: { template: "<div />" } }]
    });
    await router.push("/home");

    const store = useThemeConfig();
    store.setCollapsed(true);

    let receivedMenuCollapsed: boolean | undefined;
    let receivedMenuBreakpoint: string | undefined;
    const wrapper = mount(Aside, {
      global: {
        plugins: [router],
        stubs: {
          "a-layout-sider": { props: ["collapsed"], template: '<div class="sider-stub"><slot /></div>' },
          "a-scrollbar": { template: "<div><slot /></div>" },
          SvgIcon: { template: '<i class="svg-logo" />' },
          MenuItem: { template: "<div />" },
          "a-menu": defineComponent({
            props: ["collapsed", "breakpoint"],
            setup(props, { slots }) {
              return () => {
                receivedMenuCollapsed = props.collapsed;
                receivedMenuBreakpoint = props.breakpoint;
                return h("div", { class: "menu-stub", "data-collapsed": String(props.collapsed) }, slots.default?.());
              };
            }
          })
        }
      }
    });

    // 折叠态：Logo 隐藏标题并带 padding-unset，Menu 收到 collapsed=true 且 breakpoint='xl'
    expect(wrapper.find(".logo_box").classes()).toContain("padding-unset");
    expect(wrapper.find(".logo_title").exists()).toBe(false);
    expect(receivedMenuCollapsed).toBe(true);
    expect(receivedMenuBreakpoint).toBe("xl");

    // 切回展开态：Logo 恢复标题，Menu 收到 collapsed=false 且仍保留 breakpoint='xl'
    store.setCollapsed(false);
    await nextTick();
    expect(wrapper.find(".logo_box").classes()).not.toContain("padding-unset");
    expect(wrapper.find(".logo_title").exists()).toBe(true);
    expect(receivedMenuCollapsed).toBe(false);
    expect(receivedMenuBreakpoint).toBe("xl");
    wrapper.unmount();
  });
});
