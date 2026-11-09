import { flushPromises, mount } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { describe, expect, it, vi } from "vitest";
import type { ReplayFormState } from "./replay-form-model";
import ReplayCreateForm from "./replay-create-form.vue";

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
});

describe("回放表单的区间", () => {
  it("选的任务都是日线时，默认的整点区间吸附成整天再提交", async () => {
    const form: ReplayFormState = {
      source: "definition",
      strategy_id: "s1",
      dsl_yaml: "",
      instance_id: "",
      single_market: "spot",
      tasks: { spot: "ftask_d" },
      range: [Date.parse("2026-10-08T10:00:00Z"), Date.parse("2026-11-07T10:00:00Z")],
      spot_fee_bps: 10,
      swap_fee_bps: 10,
      funding_column: ""
    };
    const wrapper = mount(ReplayCreateForm, {
      props: {
        modelValue: form,
        strategies: [{ strategy_id: "s1", name: "日线", dsl_yaml: "name: a\n" }] as never,
        tasks: [{ task_id: "ftask_d", freq: "1d" }] as never,
        tasksLoading: false
      },
      global: { plugins: [ArcoVue] }
    });
    await flushPromises();
    expect(form.range).toEqual([Date.parse("2026-10-08T00:00:00Z"), Date.parse("2026-11-08T00:00:00Z")]);
    expect(wrapper.text()).toContain("日线任务按日期回放：起止两天都含在内");
  });
});

describe("回放表单的资金费列", () => {
  const strategies = [
    { strategy_id: "m_swap", name: "成员", dsl_yaml: "name: b\nbar: 1h\nmarket: {select: swap}\n" },
    {
      strategy_id: "p",
      name: "池",
      dsl_yaml:
        "name: p\nbar: 1h\nstrategy_pool:\n  members:\n    - {id: b, strategy: m_swap}\n  allocation: {method: fixed, ratios: {b: 1}}\n  fees: {funding_column: funding_rate}\n"
    }
  ];
  const instanceForm = (strategyId: string): ReplayFormState => ({
    source: "definition",
    strategy_id: "",
    dsl_yaml: "",
    instance_id: "i1",
    instance_strategy_id: strategyId,
    single_market: "",
    tasks: { swap: "ftask_b" },
    range: [Date.parse("2026-10-08T10:00:00Z"), Date.parse("2026-11-07T10:00:00Z")],
    spot_fee_bps: 10,
    swap_fee_bps: 10,
    funding_column: ""
  });
  const mountForm = (form: ReplayFormState) =>
    mount(ReplayCreateForm, {
      props: {
        modelValue: form,
        strategies: strategies as never,
        tasks: [{ task_id: "ftask_b", freq: "1h" }] as never,
        tasksLoading: false
      },
      global: { plugins: [ArcoVue] }
    });

  it("从池实例发起时不显示资金费列，说明资金费按池 DSL 结算", async () => {
    const wrapper = mountForm(instanceForm("p"));
    await flushPromises();
    expect(wrapper.text()).not.toContain("资金费列（可选）");
    expect(wrapper.text()).toContain("资金费按池 DSL 的 fees.funding_column 结算");
  });

  it("从普通永续策略的实例发起时照常显示资金费列", async () => {
    const wrapper = mountForm(instanceForm("m_swap"));
    await flushPromises();
    expect(wrapper.text()).toContain("资金费列（可选）");
    expect(wrapper.text()).toContain("资金费要在下面写资金费列才计入");
  });
});
