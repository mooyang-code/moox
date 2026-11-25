import { flushPromises, mount } from "@vue/test-utils";
import { computed, defineComponent, h, inject, provide } from "vue";
import { describe, expect, it, vi } from "vitest";

vi.mock("vue-router", () => ({ useRouter: () => ({ push: vi.fn() }) }));
vi.mock("@arco-design/web-vue", () => ({ Message: { success: vi.fn(), error: vi.fn() } }));
vi.mock("@/hooks/useDisplayZone", () => ({ useDisplayZone: () => computed(() => "UTC") }));
vi.mock("@/store/modules/space", () => ({ useSpaceStore: () => ({ selectedSpaceId: "" }) }));
vi.mock("@/api/storage/metadata", () => ({ listAllDatasetColumns: vi.fn(async () => []) }));
vi.mock("@/api/factor", () => ({
  getFactorParams: vi.fn(async () => ({ members: [] })),
  listFactorCategories: vi.fn(async () => [])
}));
vi.mock("@/api/strategy", () => ({
  getStrategyDisplaySettings: vi.fn(async () => ({ categories: [], members: [], columns: [] })),
  setStrategyDisplaySettings: vi.fn(),
  listStrategyResults: vi.fn(async () => ({
    items: [
      {
        result_id: "r1",
        bar_end_time: String(Date.parse("2026-10-26T10:00:00Z")),
        next_params_json: JSON.stringify({ period: 1793008800, members: { ma__var: { span: { default: 60 } } } }),
        params_delivered_at: "0",
        params_error: ""
      }
    ],
    page: { total: 1 }
  }))
}));

import StrategyFactorParams from "./strategy-factor-params.vue";

// 表格桩：每列按行渲染单元格模板，没有模板时直接显示 data-index 对应的值（与 Arco 的默认行为一致）。
const rowsKey = Symbol("rows");
const stubs = {
  "a-table": defineComponent({
    props: ["data"],
    setup(props, { slots }) {
      provide(
        rowsKey,
        computed(() => props.data ?? [])
      );
      return () => h("table", slots.columns?.());
    }
  }),
  "a-table-column": defineComponent({
    props: ["title", "dataIndex"],
    setup(props, { slots }) {
      const rows = inject(rowsKey) as { value: Record<string, unknown>[] };
      return () =>
        h(
          "tr",
          { "data-title": props.title },
          rows.value.map(record =>
            h("td", slots.cell ? slots.cell({ record }) : String(props.dataIndex ? record[props.dataIndex] : ""))
          )
        );
    }
  })
};

describe("strategy factor params", () => {
  it("shows the result period of produced params as a formatted time, not raw milliseconds", async () => {
    const instance = {
      instance_id: "i1",
      session_id: "s1",
      resolved_json: JSON.stringify({
        tasks: { spot: { factor_task_id: "t1", dataset_id: "d1" } },
        factor_params: { ma__var: [] }
      })
    };
    const wrapper = mount(StrategyFactorParams, { props: { instance } as never, global: { stubs } });
    await flushPromises();
    const cell = wrapper.find('tr[data-title="结果周期"] td');
    expect(cell.text()).toBe("2026-10-26 10:00:00 UTC");
  });
});
