import { createPinia, setActivePinia } from "pinia";
import { mount } from "@vue/test-utils";
import { nextTick } from "vue";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useStrategyStore } from "@/store/modules/strategy";
import StrategyOperationPanel from "./strategy-operation-panel.vue";

vi.mock("@/store/modules/user-info", () => ({
  useUserInfoStore: () => ({ account: { roles: ["admin"] } })
}));

describe("strategy operation capability", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
  });

  it("fails closed and renders Live only when the server capability is enabled", async () => {
    const wrapper = mount(StrategyOperationPanel, {
      props: { bindingId: "binding-1", currentMode: "observe" },
      global: {
        stubs: {
          "a-card": { template: "<section><slot /></section>" },
          "a-space": { template: "<div><slot /></div>" },
          "a-select": { props: ["modelValue"], template: '<select :data-value="modelValue"><slot /></select>' },
          "a-option": { template: "<option><slot /></option>" },
          "a-button": { template: "<button><slot /></button>" },
          "a-modal": true,
          "a-textarea": true,
          "a-alert": true
        }
      }
    });
    const store = useStrategyStore();

    expect(wrapper.text()).not.toContain("Live");
    await wrapper.setProps({ currentMode: "paper" });
    expect(wrapper.find("select").attributes("data-value")).toBe("paper");
    store.liveExecutionEnabled = true;
    await nextTick();
    expect(wrapper.text()).toContain("Live");
    store.liveExecutionEnabled = false;
    await nextTick();
    expect(wrapper.text()).not.toContain("Live");
  });
});
