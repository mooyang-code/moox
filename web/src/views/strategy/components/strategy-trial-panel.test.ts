import { flushPromises, mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";

const { validateStrategy, listFactorTasks, getStrategy } = vi.hoisted(() => ({
  validateStrategy: vi.fn(),
  listFactorTasks: vi.fn(),
  getStrategy: vi.fn()
}));
vi.mock("@/api/strategy", () => ({ validateStrategy, getStrategy }));
vi.mock("@/api/factor", () => ({ listFactorTasks }));
vi.mock("@/api/storage/metadata", () => ({
  listAllDatasets: vi.fn(async () => [{ dataset_id: "ds_uni", attributes: { market_type: "multi" } }])
}));

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

  it("does not offer tasks on a unified dataset: the panel registers tasks by market", async () => {
    listFactorTasks.mockResolvedValue({
      factor_tasks: [
        taskInfo("ftask_spot"),
        { factor_task: { ...taskInfo("ftask_uni").factor_task, source_dataset_id: "ds_uni" }, members: [] }
      ],
      page_result: { has_more: false }
    });
    const wrapper = mount(TrialPanel, {
      props: { source: "name: a\nmarket: {select: swap}\n", spaceId: "s1" },
      global: { stubs }
    });
    await (wrapper.vm as unknown as { ensureTasks: () => Promise<void> }).ensureTasks();
    await flushPromises();
    const options = wrapper
      .findAll("option")
      .map(item => item.attributes("value"))
      .filter(Boolean);
    expect(options).toEqual(["ftask_spot"]);
  });

  it("策略池按成员所需市场的并集选任务：成员一个现货、一个永续时两个市场都要选", async () => {
    vi.useFakeTimers();
    try {
      getStrategy.mockReset().mockImplementation(async (id: string) => ({
        dsl_yaml: id === "m_spot" ? "name: a\nmarket: {select: spot}\n" : "name: b\nmarket: {select: swap}\n"
      }));
      const source =
        "name: p\nbar: 1h\nstrategy_pool:\n  members:\n    - {id: a, strategy: m_spot}\n    - {id: b, strategy: m_swap}\n";
      const wrapper = mount(TrialPanel, { props: { source, spaceId: "s1" }, global: { stubs } });
      expect(wrapper.text()).toContain("成员定义读到后才能确定");
      await vi.advanceTimersByTimeAsync(500);
      await flushPromises();
      expect(getStrategy).toHaveBeenCalledTimes(2);
      expect(wrapper.text()).toContain("策略池要绑定各成员所需市场的并集：现货、永续");
      expect(wrapper.findAll("select")).toHaveLength(2);
    } finally {
      vi.useRealTimers();
    }
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

  it("DSL 改动后保留旧结果并标明对应修改前的 DSL，重新试算后换成新结果", async () => {
    validateStrategy.mockResolvedValueOnce(passed("第一次结果")).mockResolvedValueOnce(passed("第二次结果"));
    const wrapper = mount(TrialPanel, { props: { source: "name: a", spaceId: "s1" }, global: { stubs } });
    await wrapper.find("button").trigger("click");
    await flushPromises();
    expect(wrapper.text()).toContain("第一次结果");
    await wrapper.setProps({ source: "name: b" });
    expect(wrapper.text()).toContain("第一次结果");
    expect(wrapper.text()).toContain("对应修改前的 DSL");
    await wrapper.find("button").trigger("click");
    await flushPromises();
    expect(wrapper.text()).toContain("第二次结果");
    expect(wrapper.text()).not.toContain("对应修改前的 DSL");
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
