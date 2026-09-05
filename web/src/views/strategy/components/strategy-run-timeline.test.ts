import { mount } from "@vue/test-utils";
import { defineComponent, h, provide, inject } from "vue";
import { describe, expect, it } from "vitest";
import type { StrategyResult } from "@/api/strategy-types";
import Timeline from "./strategy-run-timeline.vue";

describe("strategy result delivery status", () => {
  it.each([
    ["sent", "Broker 已确认"],
    ["pending", "待投递"],
    ["cancelled", "已取消投递"],
    ["none", "无需投递"]
  ])("renders %s without implying a trade completed", (status, label) => {
    const result = { result_id: "result-1", publish_status: status, targets: [] } as unknown as StrategyResult;
    const wrapper = mount(Timeline, {
      props: { results: [result] },
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
          "a-empty": true
        }
      }
    });
    expect(wrapper.text()).toContain(label);
    expect(wrapper.text()).not.toMatch(/交易成功|已成交/);
  });
});
