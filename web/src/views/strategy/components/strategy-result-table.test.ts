import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { defineComponent, h, inject, provide } from "vue";
import { beforeEach, describe, expect, it } from "vitest";
import type { StrategyResult } from "@/api/strategy-types";
import ResultTable from "./strategy-result-table.vue";

const stubs = {
  "a-table": defineComponent({
    props: ["data"],
    setup(props, { slots }) {
      provide("rows", props.data);
      return () => h("div", slots.columns?.());
    }
  }),
  "a-table-column": defineComponent({
    setup(_, { slots }) {
      const rows = inject<StrategyResult[]>("rows", []);
      return () =>
        h(
          "div",
          rows.map(record => slots.cell?.({ record }))
        );
    }
  }),
  "a-tag": { template: "<span><slot /></span>" },
  "a-radio-group": { template: "<div><slot /></div>" },
  "a-radio": { template: "<span><slot /></span>" },
  "a-button": { template: "<button><slot /></button>" },
  "a-drawer": true,
  "a-empty": true
};

function result(overrides: Partial<StrategyResult>): StrategyResult {
  return {
    result_id: "result-1",
    instance_id: "i",
    session_id: "s",
    bar_end_time: "",
    valid_until: "",
    status: "ok",
    skip_reason: "",
    dsl_hash: "",
    targets: [],
    summary_json: "{}",
    input_json: "{}",
    rule_states_json: "{}",
    publish_status: "none",
    created_at: "",
    ...overrides
  };
}

describe("strategy result table", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it.each([
    ["sent", "已发送"],
    ["pending", "待投递"],
    ["cancelled", "已取消"],
    ["none", "无需投递"]
  ])("renders %s without implying a trade completed", (status, label) => {
    const wrapper = mount(ResultTable, {
      props: { results: [result({ publish_status: status })], total: 1, page: 1, pageSize: 20, scope: "session" },
      global: { stubs }
    });
    expect(wrapper.text()).toContain(label);
    expect(wrapper.text()).not.toMatch(/交易成功|已成交/);
  });

  it("shows skipped periods with a readable reason and ok periods with gross and cash", () => {
    const wrapper = mount(ResultTable, {
      props: {
        results: [
          result({ result_id: "r1", status: "skipped", skip_reason: "previous_version_unknown" }),
          result({
            result_id: "r2",
            status: "ok",
            summary_json: '{"gross":"0.8","cash":"0.2","turnover":"0.1"}',
            targets: [{ instrument_id: "BTC-USDT", target_weight: "0.8" }]
          })
        ],
        total: 2,
        page: 1,
        pageSize: 20,
        scope: "all"
      },
      global: { stubs }
    });
    expect(wrapper.text()).toContain("上一根版本未知");
    expect(wrapper.text()).toContain("80.00%");
    expect(wrapper.text()).toContain("20.00%");
    expect(wrapper.text()).toContain("解释");
  });
});
