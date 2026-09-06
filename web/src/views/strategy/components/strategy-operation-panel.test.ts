import { mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";

const { setInstanceEnabled } = vi.hoisted(() => ({ setInstanceEnabled: vi.fn() }));
vi.mock("@/api/strategy", () => ({ setInstanceEnabled }));
vi.mock("@arco-design/web-vue", () => ({ Message: { success: vi.fn(), error: vi.fn() } }));

import StrategyOperationPanel from "./strategy-operation-panel.vue";

describe("strategy instance controls", () => {
  beforeEach(() => setInstanceEnabled.mockReset().mockResolvedValue({}));

  it("shows an enable action for a disabled instance", () => {
    const wrapper = mount(StrategyOperationPanel, { props: { instanceId: "i-1", enabled: false }, global: { stubs: { "a-popconfirm": { template: "<div><slot /></div>" }, "a-button": { template: "<button><slot /></button>" } } } });
    expect(wrapper.text()).toContain("启用实例");
    expect(wrapper.text()).toContain("不执行清仓");
  });

  it("calls the modern enable endpoint only", async () => {
    const wrapper = mount(StrategyOperationPanel, { props: { instanceId: "i-1", enabled: false }, global: { stubs: { "a-popconfirm": { emits: ["ok"], template: "<div @click=\"$emit('ok')\"><slot /></div>" }, "a-button": { template: "<button><slot /></button>" } } } });
    await wrapper.find("button").trigger("click");
    expect(setInstanceEnabled).toHaveBeenCalledWith("i-1", true);
  });
});
