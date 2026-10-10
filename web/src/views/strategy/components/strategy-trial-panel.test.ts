import { flushPromises, mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";

const { validateStrategy } = vi.hoisted(() => ({ validateStrategy: vi.fn() }));
vi.mock("@/api/strategy", () => ({ validateStrategy }));
vi.mock("@/api/storage/metadata", () => ({ listViews: vi.fn().mockResolvedValue({ views: [], page_result: { has_more: false } }) }));

import TrialPanel from "./strategy-trial-panel.vue";

const stubs = {
  "a-select": true,
  "a-option": true,
  "a-tabs": true,
  "a-tab-pane": true,
  "a-space": true,
  "a-tag": true,
  ResultItems: true,
  ResolvedTable: true,
  "a-button": { emits: ["click"], template: "<button @click=\"$emit('click')\"><slot /></button>" },
  "a-alert": { template: "<div class=\"alert\"><slot /></div>" }
};

function passed(message: string) {
  return { diagnostics: [message], resolved_json: "", trial: null, trial_items: [] };
}

describe("strategy trial panel", () => {
  beforeEach(() => validateStrategy.mockReset());

  it("drops a response that arrives after the DSL changed", async () => {
    let resolve!: (value: unknown) => void;
    validateStrategy.mockReturnValue(new Promise(r => (resolve = r)));
    const wrapper = mount(TrialPanel, { props: { source: "name: a", spaceId: "s1" }, global: { stubs } });
    await wrapper.find("button").trigger("click");
    await wrapper.setProps({ source: "name: b" });
    resolve(passed("旧输入的结果"));
    await flushPromises();
    expect(wrapper.text()).not.toContain("旧输入的结果");
  });

  it("clears a finished result when the DSL changes and accepts the next run", async () => {
    validateStrategy.mockResolvedValueOnce(passed("第一次结果")).mockResolvedValueOnce(passed("第二次结果"));
    const wrapper = mount(TrialPanel, { props: { source: "name: a", spaceId: "s1" }, global: { stubs } });
    await wrapper.find("button").trigger("click");
    await flushPromises();
    expect(wrapper.text()).toContain("第一次结果");
    await wrapper.setProps({ source: "name: b" });
    expect(wrapper.text()).not.toContain("第一次结果");
    await wrapper.find("button").trigger("click");
    await flushPromises();
    expect(wrapper.text()).toContain("第二次结果");
  });
});
