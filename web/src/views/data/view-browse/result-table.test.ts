import { mount } from "@vue/test-utils";
import { defineComponent, h, inject, provide, ref, toRef, type Ref } from "vue";
import { describe, expect, it } from "vitest";
import ResultTable from "./result-table.vue";

// 表格按行渲染每一列的标题与 cell 插槽，便于断言显示的文字。
const TableStub = defineComponent({
  props: { data: { type: Array, default: () => [] } },
  setup(props, { slots }) {
    provide("rows", toRef(props, "data"));
    return () => h("div", slots.columns?.());
  }
});
const ColumnStub = defineComponent({
  props: { title: { type: String, default: "" } },
  setup(props, { slots }) {
    const rows = inject<Ref<unknown[]>>("rows", ref([]));
    return () =>
      h("div", [
        slots.title?.() ?? props.title,
        ...rows.value.map((record, rowIndex) =>
          slots.cell ? slots.cell({ record, rowIndex }) : String((record as Record<string, unknown>).version ?? "")
        )
      ]);
  }
});

function mountTable(mode: "time_series" | "record", version: string) {
  return mount(ResultTable, {
    props: {
      mode,
      rows: [{ id: "r1", key: "k1", version, values: { close: "1" } }],
      columnNames: ["close"],
      allColumnNames: ["close"],
      columnTitle: (name: string) => (name === "close" ? "收盘价" : name),
      sortField: "",
      sortDirection: "",
      page: 1,
      pageSize: 25,
      zone: "UTC"
    },
    global: {
      stubs: {
        "a-table": TableStub,
        "a-table-column": ColumnStub,
        "a-button": { template: "<button><slot /></button>" },
        "a-tooltip": true,
        "a-modal": true,
        "a-empty": true,
        "icon-left": true,
        "icon-right": true
      }
    }
  });
}

describe("结果表格", () => {
  it("记录模式的版本是任意字符串，原样显示；时序模式的时间按时区格式化", () => {
    const record = mountTable("record", "v1");
    expect(record.text()).toContain("记录ID");
    expect(record.text()).toContain("v1");
    record.unmount();

    const series = mountTable("time_series", "2026-01-01T00:00:00Z");
    expect(series.text()).toContain("收盘价");
    expect(series.text()).toContain("2026-01-01 00:00:00");
    series.unmount();
  });
});
