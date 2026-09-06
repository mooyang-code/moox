import fs from "node:fs";
import path from "node:path";
import { mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";

const { setInstanceEnabled } = vi.hoisted(() => ({ setInstanceEnabled: vi.fn() }));
vi.mock("@/api/strategy", () => ({ setInstanceEnabled }));

import StrategyOperationPanel from "./strategy-operation-panel.vue";

describe("Strategy instance controls", () => {
  beforeEach(() => {
    setInstanceEnabled.mockReset().mockResolvedValue({});
  });

  it("only offers instance enable when disabled", async () => {
    const wrapper = mount(StrategyOperationPanel, {
      props: { instanceId: "instance-1", enabled: false },
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

  it("uses only the instance enabled contract", () => {
    const source = fs.readFileSync(
      path.resolve(process.cwd(), "src/views/strategy/components/strategy-operation-panel.vue"),
      "utf8"
    );
    expect(source).toContain("setInstanceEnabled");
    expect(source).not.toContain("setRunnerStatus");

    const running = fs.readFileSync(path.resolve(process.cwd(), "src/views/strategy/running/index.vue"), "utf8");
    expect(running).toContain("loadInstances");
    expect(running).toContain("input_bindings_json");
    expect(running).toContain("新增实例");
  });
});
