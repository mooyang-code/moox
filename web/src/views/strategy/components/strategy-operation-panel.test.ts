import { mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";

const { setRunnerStatus } = vi.hoisted(() => ({ setRunnerStatus: vi.fn() }));
vi.mock("@/api/strategy", () => ({ setRunnerStatus }));

import StrategyOperationPanel from "./strategy-operation-panel.vue";

describe("Strategy Runner controls", () => {
  beforeEach(() => setRunnerStatus.mockReset().mockResolvedValue({}));

  it("only offers Runner enable when disabled", async () => {
    const wrapper = mount(StrategyOperationPanel, {
      props: { runnerId: "runner-1", status: "disabled" },
      global: {
        stubs: {
          "a-space": { template: "<div><slot /></div>" },
          "a-popconfirm": { template: "<div><slot /></div>" },
          "a-button": { template: "<button><slot /></button>" }
        }
      }
    });
    expect(wrapper.text()).toContain("启用");
    expect(wrapper.text()).not.toContain("执行模式");
    expect(wrapper.text()).not.toContain("Exchange Account");
  });
});
