import { flushPromises, mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";

const { setInstanceEnabled, confirm } = vi.hoisted(() => ({ setInstanceEnabled: vi.fn(), confirm: vi.fn() }));
vi.mock("@/api/strategy", () => ({ setInstanceEnabled }));
vi.mock("@arco-design/web-vue", () => ({
  Message: { success: vi.fn(), error: vi.fn(), info: vi.fn(), loading: vi.fn(() => ({ close: vi.fn() })) },
  Modal: { confirm }
}));

import StrategyOperationPanel from "./strategy-operation-panel.vue";

const stubs = { "a-button": { template: "<button><slot /></button>" } };

describe("strategy instance controls", () => {
  beforeEach(() => {
    setInstanceEnabled.mockReset().mockResolvedValue({});
    confirm.mockReset();
  });

  it("shows an enable action for a disabled instance", () => {
    const wrapper = mount(StrategyOperationPanel, { props: { instanceId: "i-1", enabled: false }, global: { stubs } });
    expect(wrapper.text()).toContain("启用实例");
    expect(wrapper.text()).toContain("不执行清仓");
  });

  it("opens an explicit confirm dialog and sends nothing until confirmed", async () => {
    const wrapper = mount(StrategyOperationPanel, { props: { instanceId: "i-1", enabled: true }, global: { stubs } });
    await wrapper.find("button").trigger("click");
    expect(confirm).toHaveBeenCalledTimes(1);
    expect(setInstanceEnabled).not.toHaveBeenCalled();
    confirm.mock.calls[0][0].onCancel();
    expect(wrapper.emitted("started")).toBeUndefined();
  });

  it("calls the enable endpoint after confirming and reports the lifecycle", async () => {
    const wrapper = mount(StrategyOperationPanel, { props: { instanceId: "i-1", enabled: false }, global: { stubs } });
    await wrapper.find("button").trigger("click");
    confirm.mock.calls[0][0].onOk();
    await flushPromises();
    expect(setInstanceEnabled).toHaveBeenCalledWith("i-1", true);
    expect(wrapper.emitted("started")).toHaveLength(1);
    expect(wrapper.emitted("changed")).toHaveLength(1);
    expect(wrapper.emitted("failed")).toBeUndefined();
  });

  it("emits failed when the request fails", async () => {
    setInstanceEnabled.mockRejectedValue(new Error("boom"));
    const wrapper = mount(StrategyOperationPanel, { props: { instanceId: "i-1", enabled: true }, global: { stubs } });
    await wrapper.find("button").trigger("click");
    confirm.mock.calls[0][0].onOk();
    await flushPromises();
    expect(wrapper.emitted("failed")).toHaveLength(1);
    expect(wrapper.emitted("changed")).toHaveLength(1);
  });

  it("does not open the dialog while the state is uncertain", async () => {
    const wrapper = mount(StrategyOperationPanel, {
      props: { instanceId: "i-1", enabled: true, uncertain: true },
      global: { stubs }
    });
    await wrapper.find("button").trigger("click");
    expect(confirm).not.toHaveBeenCalled();
  });
});
