import { mount } from "@vue/test-utils";
import { defineComponent, h, provide, inject } from "vue";
import { describe, expect, it } from "vitest";
import type { StrategyResult } from "@/api/strategy-types";
import ResultTable from "./strategy-result-table.vue";

describe("strategy result delivery status", () => {
  it.each([
    ["sent", "已发送"],
    ["pending", "待投递"],
    ["cancelled", "已取消"],
    ["none", "无需投递"]
  ])("renders %s without implying a trade completed", (status, label) => {
    const result = { result_id: "result-1", publish_status: status, targets: [] } as unknown as StrategyResult;
    const wrapper = mount(ResultTable, {
      props: { results: [result], total: 1, page: 1, pageSize: 20, scope: "session" },
      global: {
        stubs: {
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
          "a-modal": { template: "<div><slot /></div>" },
          "a-empty": true
        }
      }
    });
    expect(wrapper.text()).toContain(label);
    expect(wrapper.text()).not.toMatch(/交易成功|已成交/);
  });
});
