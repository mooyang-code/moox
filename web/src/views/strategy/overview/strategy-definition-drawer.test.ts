import { flushPromises, mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { nextTick, reactive } from "vue";

const api = vi.hoisted(() => ({ getStrategy: vi.fn() }));
vi.mock("@/api/strategy", () => api);
const space = vi.hoisted(() => ({ store: null as any }));
vi.mock("@/store/modules/space", () => ({ useSpaceStore: () => space.store }));
vi.mock("@arco-design/web-vue", () => ({ Message: { success: vi.fn(), warning: vi.fn(), error: vi.fn() } }));

import Drawer from "./strategy-definition-drawer.vue";

const stubs = {
  "a-drawer": { props: ["visible"], template: '<div v-if="visible" class="drawer"><slot /></div>' },
  "a-alert": { template: '<div class="alert"><slot /><slot name="action" /></div>' },
  "a-spin": { template: "<div><slot /></div>" },
  "a-descriptions": { template: "<div><slot /></div>" },
  "a-descriptions-item": { template: "<div><slot /></div>" },
  "a-space": { template: "<div><slot /></div>" },
  "a-button": { template: "<button><slot /></button>" },
  CodeBlock: { props: ["code"], template: '<pre class="code">{{ code }}</pre>' }
};

function definition(id: string) {
  return { strategy_id: id, name: `策略-${id}`, dsl_yaml: `name: ${id}`, dsl_hash: "sha256:abc", created_at: "", updated_at: "" };
}
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>(ok => (resolve = ok));
  return { promise, resolve };
}

describe("strategy definition drawer", () => {
  beforeEach(() => {
    api.getStrategy.mockReset();
    space.store = reactive({ selectedSpaceId: "crypto" });
  });

  it("stays closed and does not read anything without an id", async () => {
    const wrapper = mount(Drawer, { props: { definitionId: "" }, global: { stubs } });
    await flushPromises();
    expect(wrapper.find(".drawer").exists()).toBe(false);
    expect(api.getStrategy).not.toHaveBeenCalled();
  });

  it("shows the definition and emits the context actions", async () => {
    api.getStrategy.mockResolvedValue(definition("s1"));
    const wrapper = mount(Drawer, { props: { definitionId: "s1" }, global: { stubs } });
    await flushPromises();
    expect(wrapper.text()).toContain("策略-s1");
    expect(wrapper.find(".code").text()).toBe("name: s1");
    const buttons = wrapper.findAll("button");
    await buttons.find(item => item.text() === "创建实例")!.trigger("click");
    await buttons.find(item => item.text() === "发起回放")!.trigger("click");
    await buttons.find(item => item.text() === "查看当前空间实例")!.trigger("click");
    await buttons.find(item => item.text() === "编辑定义")!.trigger("click");
    expect(wrapper.emitted("createInstance")).toEqual([["s1"]]);
    expect(wrapper.emitted("replay")).toEqual([["s1"]]);
    expect(wrapper.emitted("instances")).toEqual([["s1"]]);
    expect(wrapper.emitted("edit")).toEqual([["s1"]]);
  });

  it("reports a read failure with a retry that works", async () => {
    api.getStrategy.mockRejectedValueOnce(new Error("网络错误")).mockResolvedValueOnce(definition("s1"));
    const wrapper = mount(Drawer, { props: { definitionId: "s1" }, global: { stubs } });
    await flushPromises();
    expect(wrapper.find(".alert").text()).toContain("策略定义读取失败：网络错误");
    await wrapper.find(".alert button").trigger("click");
    await flushPromises();
    expect(wrapper.find(".alert").exists()).toBe(false);
    expect(wrapper.text()).toContain("策略-s1");
  });

  it("discards a late response after switching to another definition", async () => {
    const first = deferred<ReturnType<typeof definition>>();
    api.getStrategy.mockReturnValueOnce(first.promise).mockResolvedValueOnce(definition("s2"));
    const wrapper = mount(Drawer, { props: { definitionId: "s1" }, global: { stubs } });
    await wrapper.setProps({ definitionId: "s2" });
    await flushPromises();
    first.resolve(definition("s1"));
    await flushPromises();
    expect(wrapper.text()).toContain("策略-s2");
    expect(wrapper.text()).not.toContain("策略-s1");
  });
  it("re-reads the definition after a space switch and ignores a stale-space cancellation", async () => {
    const { StaleSpaceRequestError } = await import("@/api/admin/stale-space");
    api.getStrategy.mockRejectedValueOnce(new StaleSpaceRequestError()).mockResolvedValueOnce(definition("s1"));
    const wrapper = mount(Drawer, { props: { definitionId: "s1" }, global: { stubs } });
    await flushPromises();
    expect(wrapper.find(".alert").exists()).toBe(false);
    space.store.selectedSpaceId = "hk";
    await nextTick();
    await flushPromises();
    expect(api.getStrategy).toHaveBeenCalledTimes(2);
    expect(wrapper.text()).toContain("策略-s1");
  });
});
