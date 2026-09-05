import fs from "node:fs";
import path from "node:path";
import { mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";

const { setRunnerStatus, setInstanceEnabled } = vi.hoisted(() => ({ setRunnerStatus: vi.fn(), setInstanceEnabled: vi.fn() }));
vi.mock("@/api/strategy", () => ({ setRunnerStatus, setInstanceEnabled }));

import StrategyOperationPanel from "./strategy-operation-panel.vue";

describe("Strategy Runner controls", () => {
  beforeEach(() => {
    setRunnerStatus.mockReset().mockResolvedValue({});
    setInstanceEnabled.mockReset().mockResolvedValue({});
  });

  it("only offers Runner enable when disabled", async () => {
    const wrapper = mount(StrategyOperationPanel, {
      props: { runnerId: "runner-1", status: "DISABLED" },
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

  it("uses the instance enabled contract and keeps the legacy adapter", () => {
    const source = fs.readFileSync(
      path.resolve(process.cwd(), "src/views/strategy/components/strategy-operation-panel.vue"),
      "utf8"
    );
    expect(source).toContain("setInstanceEnabled");
    expect(source).toContain("setRunnerStatus");
    expect(source).toContain("enabled ? \"ENABLED\" : \"DISABLED\"");

    const running = fs.readFileSync(path.resolve(process.cwd(), "src/views/strategy/running/index.vue"), "utf8");
    expect(running).toContain("loadInstances");
    expect(running).toContain("input_bindings_json");
    expect(running).toContain("新增实例");
  });
});
