import { flushPromises, mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";

const api = vi.hoisted(() => ({ getInstance: vi.fn() }));
vi.mock("@/api/strategy", () => api);

import Drawer from "./instance-quick-drawer.vue";

const stubs = {
  "a-drawer": {
    props: ["visible"],
    emits: ["cancel", "close"],
    template:
      '<div v-if="visible" class="drawer"><button class="x" @click="$emit(\'cancel\')" /><button class="anim" @click="$emit(\'close\')" /><slot /></div>'
  },
  "a-alert": { template: '<div class="alert"><slot /><slot name="action" /></div>' },
  "a-spin": { template: "<div><slot /></div>" },
  "a-descriptions": { template: "<div><slot /></div>" },
  "a-descriptions-item": { template: "<div><slot /></div>" },
  "a-space": { template: "<div><slot /></div>" },
  "a-button": { template: "<button><slot /></button>" },
  StatusBadge: true
};
const strategies = [{ strategy_id: "s1", name: "动量" }] as never;

function instance(id: string) {
  return {
    instance_id: id,
    strategy_id: "s1",
    space_id: "crypto",
    view_id: "view_a",
    logical_account_id: "",
    enabled: false,
    session_id: "",
    resolved_json: "{}",
    health: "ok",
    created_at: "",
    updated_at: ""
  };
}

describe("instance quick drawer", () => {
  beforeEach(() => api.getInstance.mockReset());

  it("reads only the instance and shows the read time", async () => {
    api.getInstance.mockResolvedValue(instance("i1"));
    const wrapper = mount(Drawer, { props: { instanceId: "i1", strategies }, global: { stubs } });
    await flushPromises();
    expect(api.getInstance).toHaveBeenCalledTimes(1);
    expect(wrapper.text()).toContain("i1");
    expect(wrapper.text()).toContain("动量");
    expect(wrapper.text()).toMatch(/\d{2}:\d{2}:\d{2}/);
  });

  it("emits the open-detail and replay actions", async () => {
    api.getInstance.mockResolvedValue(instance("i1"));
    const wrapper = mount(Drawer, { props: { instanceId: "i1", strategies }, global: { stubs } });
    await flushPromises();
    const buttons = wrapper.findAll("button");
    await buttons.find(item => item.text() === "打开完整详情")!.trigger("click");
    await buttons.find(item => item.text() === "按实例会话回放")!.trigger("click");
    expect(wrapper.emitted("openDetail")).toEqual([["i1"]]);
    expect(wrapper.emitted("replay")![0][0]).toMatchObject({ instance_id: "i1", view_id: "view_a" });
  });

  it("shows a failure with retry and clears the previous object when switching", async () => {
    api.getInstance
      .mockResolvedValueOnce(instance("i1"))
      .mockRejectedValueOnce(new Error("超时"))
      .mockResolvedValueOnce(instance("i2"));
    const wrapper = mount(Drawer, { props: { instanceId: "i1", strategies }, global: { stubs } });
    await flushPromises();
    await wrapper.setProps({ instanceId: "i2" });
    await flushPromises();
    expect(wrapper.find(".alert").text()).toContain("实例读取失败：超时");
    expect(wrapper.text()).not.toContain("i1");
    await wrapper.find(".alert button").trigger("click");
    await flushPromises();
    expect(wrapper.text()).toContain("i2");
  });

  it("stays closed without an id", async () => {
    const wrapper = mount(Drawer, { props: { instanceId: "", strategies }, global: { stubs } });
    await flushPromises();
    expect(wrapper.find(".drawer").exists()).toBe(false);
    expect(api.getInstance).not.toHaveBeenCalled();
  });
  it("closes on the user's cancel but ignores the drawer's close-animation event", async () => {
    api.getInstance.mockResolvedValue(instance("i1"));
    const wrapper = mount(Drawer, { props: { instanceId: "i1", strategies }, global: { stubs } });
    await flushPromises();
    await wrapper.find(".anim").trigger("click");
    expect(wrapper.emitted("close")).toBeUndefined();
    await wrapper.find(".x").trigger("click");
    expect(wrapper.emitted("close")).toHaveLength(1);
  });
});
