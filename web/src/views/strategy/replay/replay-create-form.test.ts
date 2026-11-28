import { flushPromises, mount } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { describe, expect, it, vi } from "vitest";
import { reactive } from "vue";
import type { ReplayFormState } from "./replay-form-model";
import ReplayCreateForm from "./replay-create-form.vue";
import { registerFixtureDefinitions } from "../../../../tests/strategy-requirement";

// 挂载前把表单的定义列表登记给要求的模拟（页面按定义 ID 向后端读要求）
function mountWithDefinitions(options: { props: { strategies: unknown }; global?: Record<string, unknown> }) {
  registerFixtureDefinitions(options.props.strategies as { strategy_id: string; dsl_yaml?: string }[]);
  return mount(ReplayCreateForm, options as never);
}

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
      slippage_bps: 0,
      funding_column: "",
      input: "view"
    };
    const wrapper = mountWithDefinitions({
      props: {
        modelValue: form,
        strategies: [{ strategy_id: "s1", name: "日线", dsl_yaml: "name: a\n" }] as never,
        tasks: [{ task_id: "ftask_d", frequency: "1d" }] as never,
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
    slippage_bps: 0,
    funding_column: "",
    input: "view"
  });
  const mountForm = (form: ReplayFormState) =>
    mountWithDefinitions({
      props: {
        modelValue: form,
        strategies: strategies as never,
        tasks: [{ task_id: "ftask_b", frequency: "1h" }] as never,
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

describe("回放表单的因子数据来源", () => {
  const tunedDSL = 'name: t\nfactor_params:\n  ma__var:\n    window: {default: "close"}\nrules: []\n';
  const mountForm = (form: ReplayFormState, dsl: string) =>
    mountWithDefinitions({
      props: {
        modelValue: form,
        strategies: [{ strategy_id: "s1", name: "策略", dsl_yaml: dsl }] as never,
        tasks: [{ task_id: "ftask_a", frequency: "1h" }] as never,
        tasksLoading: false
      },
      global: { plugins: [ArcoVue] }
    });
  const base = (): ReplayFormState => ({
    source: "definition",
    strategy_id: "s1",
    dsl_yaml: "",
    instance_id: "",
    single_market: "spot",
    tasks: { spot: "ftask_a" },
    range: [Date.parse("2026-10-08T10:00:00Z"), Date.parse("2026-11-07T10:00:00Z")],
    spot_fee_bps: 10,
    swap_fee_bps: 10,
    slippage_bps: 0,
    funding_column: "",
    input: "view"
  });

  it("声明了 factor_params 的策略自动切到因子现算，结果 View 不能选", async () => {
    const form = base();
    const wrapper = mountForm(form, tunedDSL);
    await flushPromises();
    expect(form.input).toBe("compute");
    expect(wrapper.text()).toContain("因子数据来源");
    expect(wrapper.text()).toContain("只能用因子现算");
  });

  it("普通策略默认读结果 View，选因子现算时说明它的限制", async () => {
    const form = reactive(base());
    const wrapper = mountForm(form, "name: a\n");
    await flushPromises();
    expect(form.input).toBe("view");
    expect(wrapper.text()).toContain("区间受 View 保留的根数限制");
    form.input = "compute";
    await flushPromises();
    expect(wrapper.text()).toContain("不受 View 保留根数限制");
  });

  it("读取策略要绑定的任务失败时提示读取失败并支持重试，不再误导为请选择计算任务", async () => {
    const { validateReplayForm } = await import("./replay-form-model");
    reqMock.error = "策略服务暂不可用";
    reqMock.reload.mockReset();
    try {
      const form = reactive({ ...base(), single_market: "", tasks: {} });
      const wrapper = mountForm(form, "name: a\n");
      await flushPromises();
      expect(form.requirement_error).toBe("策略服务暂不可用");
      expect(wrapper.text()).toContain("读取策略要绑定的计算任务失败：策略服务暂不可用");
      expect(validateReplayForm(form)).toBe("读取策略要绑定的计算任务失败：策略服务暂不可用");
      const retry = wrapper.findAll(".arco-link").find(link => link.text().includes("重试"));
      expect(retry).toBeDefined();
      await retry!.trigger("click");
      expect(reqMock.reload).toHaveBeenCalledTimes(1);
    } finally {
      reqMock.error = "";
    }
  });
});

const reqMock = vi.hoisted(() => ({ error: "", reload: vi.fn() }));
// 要绑定的任务由后端按 DSL 推出：测试里按 DSL 与登记的定义模拟
vi.mock("@/views/strategy/use-task-requirement", async () => {
  const { ref } = await import("vue");
  const { useFixtureTaskRequirement } = await import("../../../../tests/strategy-requirement");
  return {
    useTaskRequirement: (source: Parameters<typeof useFixtureTaskRequirement>[0]) =>
      reqMock.error
        ? { requirement: ref(null), loading: ref(false), error: ref(reqMock.error), reload: reqMock.reload }
        : useFixtureTaskRequirement(source)
  };
});
