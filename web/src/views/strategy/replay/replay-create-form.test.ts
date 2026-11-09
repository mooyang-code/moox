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
