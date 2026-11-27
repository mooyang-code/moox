import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useRoutesConfigStore } from "@/store/modules/route-config";

const routerMock = vi.hoisted(() => ({ push: vi.fn() }));
vi.mock("vue-router", () => ({ useRouter: () => routerMock }));

import Tabs from "./index.vue";

const stubs = {
  "a-tabs": {
    emits: ["delete", "tab-click"],
    template: '<div class="tabs-stub"><slot /><button class="trigger-delete-home" @click="$emit(\'delete\', \'home\')" /></div>'
  },
  "a-tab-pane": { props: ["title", "closable"], template: '<span class="tab-pane">{{ title }}</span>' },
  "a-space": { template: "<div><slot /></div>" },
  "a-tooltip": { template: "<div><slot /></div>" },
  "a-dropdown": { template: '<div><slot /><div class="menu"><slot name="content" /></div></div>' },
  "a-doption": { template: '<button class="menu-item"><slot /></button>' },
  "icon-refresh": true,
  "icon-apps": true,
  "icon-close": true,
  "icon-left": true,
  "icon-right": true,
  "icon-close-circle": true,
  "icon-folder-delete": true
};

const homeTab = { path: "/home", name: "home", meta: { title: "首页", affix: true }, query: {}, params: {} };
const tasksTab = {
  path: "/strategy/tasks",
  name: "strategy-tasks",
  meta: { title: "策略运行", affix: false },
  query: {},
  params: {}
};
const blockedTab = {
  path: "/trading/blocked",
  name: "trading-blocked",
  meta: { title: "下架名单", affix: false },
  query: {},
  params: {}
};

const clickMenu = async (wrapper: ReturnType<typeof mount>, text: string) => {
  const target = wrapper.findAll(".menu-item").find(item => item.text().trim() === text);
  if (!target) throw new Error(`未找到菜单项: ${text}`);
  await target.trigger("click");
};

describe("标签栏 Tabs", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    routerMock.push.mockReset();
  });

  it("在隐藏路由（currentRoute 为空对象且无 meta）上点“关闭全部”不抛空指针，有剩余标签页时跳转最后一个", async () => {
    const store = useRoutesConfigStore();
    store.tabsList = [homeTab, tasksTab];
    store.currentRoute = {};

    const wrapper = mount(Tabs, { global: { stubs } });
    await clickMenu(wrapper, "关闭全部");

    expect(store.tabsList.map((item: { name: string }) => item.name)).toEqual(["home"]);
    expect(routerMock.push).toHaveBeenCalledWith({ path: "/home", query: {} });
    wrapper.unmount();
  });

  it("直接进入隐藏路由且标签栏为空时点“关闭全部”和“关闭当前”均安全无报错", async () => {
    const store = useRoutesConfigStore();
    store.tabsList = [];
    store.currentRoute = {};

    const wrapper = mount(Tabs, { global: { stubs } });
    await clickMenu(wrapper, "关闭全部");
    await clickMenu(wrapper, "关闭当前");
    await clickMenu(wrapper, "关闭左侧");
    await clickMenu(wrapper, "关闭右侧");

    expect(store.tabsList).toEqual([]);
    expect(routerMock.push).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it("当前处于隐藏路由但标签栏已有其它页时，“关闭当前”与“关闭右侧”不误关或误跳", async () => {
    const store = useRoutesConfigStore();
    store.tabsList = [homeTab, tasksTab, blockedTab];
    store.currentRoute = {};

    const wrapper = mount(Tabs, { global: { stubs } });
    await clickMenu(wrapper, "关闭当前");
    await clickMenu(wrapper, "关闭右侧");
    await clickMenu(wrapper, "关闭左侧");

    expect(store.tabsList.map((item: { name: string }) => item.name)).toEqual(["home", "strategy-tasks", "trading-blocked"]);
    expect(routerMock.push).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it("在固定页（首页）上点“关闭全部”时清除可关闭标签页且不重复跳转", async () => {
    const store = useRoutesConfigStore();
    store.tabsList = [homeTab, tasksTab, blockedTab];
    store.currentRoute = homeTab;

    const wrapper = mount(Tabs, { global: { stubs } });
    await clickMenu(wrapper, "关闭全部");

    expect(store.tabsList.map((item: { name: string }) => item.name)).toEqual(["home"]);
    expect(routerMock.push).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it("在固定页（首页）上点“关闭当前”或触发删除首页时，不移除首页也不误跳转到其它标签页", async () => {
    const store = useRoutesConfigStore();
    store.tabsList = [homeTab, tasksTab, blockedTab];
    store.currentRoute = homeTab;

    const wrapper = mount(Tabs, { global: { stubs } });
    await clickMenu(wrapper, "关闭当前");
    expect(store.tabsList.map((item: { name: string }) => item.name)).toEqual(["home", "strategy-tasks", "trading-blocked"]);
    expect(routerMock.push).not.toHaveBeenCalled();

    // 即使通过标签页 @delete 事件直接删固定页也不移除、不跳转
    await wrapper.find(".trigger-delete-home").trigger("click");
    expect(store.tabsList.map((item: { name: string }) => item.name)).toEqual(["home", "strategy-tasks", "trading-blocked"]);
    expect(routerMock.push).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it("在非固定页上点“关闭当前”会移除当前标签并跳转到剩余最后一个标签", async () => {
    const store = useRoutesConfigStore();
    store.tabsList = [homeTab, tasksTab, blockedTab];
    store.currentRoute = blockedTab;

    const wrapper = mount(Tabs, { global: { stubs } });
    await clickMenu(wrapper, "关闭当前");

    expect(store.tabsList.map((item: { name: string }) => item.name)).toEqual(["home", "strategy-tasks"]);
    expect(routerMock.push).toHaveBeenCalledWith({ path: "/strategy/tasks", query: {} });
    wrapper.unmount();
  });
});
