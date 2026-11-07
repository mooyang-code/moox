import { flushPromises, mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";

const { validateStrategy, listFactorTasks } = vi.hoisted(() => ({ validateStrategy: vi.fn(), listFactorTasks: vi.fn() }));
vi.mock("@/api/strategy", () => ({ validateStrategy }));
vi.mock("@/api/factor", () => ({ listFactorTasks }));

import TrialPanel from "./strategy-trial-panel.vue";

const taskInfo = (taskId: string) => ({
  factor_task: { task_id: taskId, name: taskId, space_id: "s1", freq: "1h", status: "enabled" },
  members: []
});

const stubs = {
  "a-select": {
    props: ["modelValue"],
    emits: ["update:modelValue", "popup-visible-change"],
    template:
      '<select :value="modelValue" @change="$emit(\'update:modelValue\', $event.target.value)"><option value="" /><slot /></select>'
  },
  "a-option": { props: ["value"], template: '<option :value="value"><slot /></option>' },
  "a-tabs": true,
  "a-tab-pane": true,
  "a-space": true,
  "a-tag": true,
  ResultItems: true,
  ResolvedTable: true,
  "a-button": { emits: ["click"], template: "<button @click=\"$emit('click')\"><slot /></button>" },
  "a-alert": { template: '<div class="alert"><slot /></div>' }
};

function passed(message: string) {
  return { diagnostics: [message], resolved_json: "", trial: null, trial_items: [] };
}

describe("strategy trial panel", () => {
  beforeEach(() => {
    validateStrategy.mockReset();
    listFactorTasks
      .mockReset()
      .mockResolvedValue({ factor_tasks: [taskInfo("ftask_spot"), taskInfo("ftask_swap")], page_result: { has_more: false } });
  });

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

  it("trials a mixed-market strategy with a spot and a swap task and shows the market of each target", async () => {
    const dslMixed = "name: a\nmarket: {select: both, prefer: swap}\n";
    validateStrategy.mockResolvedValue({
      diagnostics: [],
      resolved_json: "",
      trial: {
        status: "ok",
        skip_reason: "",
        bar_end_time: "",
        targets: [{ instrument_id: "BTC-USDT", target_weight: "0.5", market: "swap" }]
      },
      trial_items: []
    });
    const shown = {
      ...stubs,
      "a-tabs": { template: "<div><slot /></div>" },
      "a-tab-pane": { template: "<div><slot /></div>" },
      "a-tag": { template: "<span><slot /></span>" },
      "a-space": { template: "<div><slot /></div>" }
    };
    const wrapper = mount(TrialPanel, { props: { source: dslMixed, spaceId: "s1" }, global: { stubs: shown } });
    await flushPromises();
    // 声明了 market：不需要选市场，只有现货、永续两个任务选择框。
    expect(wrapper.findAll("select")).toHaveLength(2);
    expect(wrapper.find("button").text()).toBe("仅校验 DSL");
    // 手动加载任务列表（真实页面由下拉打开触发）。
    (wrapper.vm as unknown as { ensureTasks: () => Promise<void> }).ensureTasks?.();
    await flushPromises();
    const [spot, swap] = wrapper.findAll("select");
    await spot.setValue("ftask_spot");
    // 选了一个市场的任务、另一个没选：不能试算。
    expect(wrapper.find("button").attributes("disabled")).toBeDefined();
    await swap.setValue("ftask_swap");
    expect(wrapper.find("button").attributes("disabled")).toBeUndefined();
    await wrapper.find("button").trigger("click");
    await flushPromises();
    expect(validateStrategy).toHaveBeenCalledWith(dslMixed, { spot: "ftask_spot", swap: "ftask_swap" });
    expect(wrapper.text()).toContain("BTC-USDT（永续）");
  });
});
