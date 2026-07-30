import fs from "node:fs";
import path from "node:path";
import { mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";

const { setRunnerStatus } = vi.hoisted(() => ({ setRunnerStatus: vi.fn() }));
vi.mock("@/api/strategy", () => ({ setRunnerStatus }));

import StrategyOperationPanel from "./strategy-operation-panel.vue";

describe("Strategy Runner controls", () => {
  beforeEach(() => setRunnerStatus.mockReset().mockResolvedValue({}));

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

  it("uses the uppercase backend status contract", () => {
    const source = fs.readFileSync(
      path.resolve(process.cwd(), "src/views/strategy/components/strategy-operation-panel.vue"),
      "utf8"
    );
    expect(source).toMatch(/status === ["']ENABLED["']/);
    expect(source).toMatch(/change\(["']DISABLED["']\)/);
    expect(source).toMatch(/change\(["']ENABLED["']\)/);
    expect(source).toContain(`"ENABLED" | "DISABLED"`);

    const running = fs.readFileSync(path.resolve(process.cwd(), "src/views/strategy/running/index.vue"), "utf8");
    expect(running).toContain(`<a-option value="ENABLED">ENABLED</a-option>`);
    expect(running).toContain(`<a-option value="DISABLED">DISABLED</a-option>`);
    expect(running).toContain(`status: "DISABLED"`);
  });
});
