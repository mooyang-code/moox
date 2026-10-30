import { flushPromises, shallowMount } from "@vue/test-utils";
import { defineComponent, h, inject, provide, ref, toRef, type Ref } from "vue";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  catalog: vi.fn(),
  columns: vi.fn(),
  queryTimeSeries: vi.fn()
}));

vi.mock("./composables/view-browse-catalog", () => ({ loadTargetedViewCatalog: mocks.catalog }));
vi.mock("@/api/storage/metadata", () => ({
  listDatasetColumns: vi.fn().mockResolvedValue({ columns: [] }),
  listAllViewColumns: mocks.columns,
  listFields: vi.fn().mockResolvedValue({ fields: [] }),
  listAllDatasets: vi.fn(),
  listAllViews: vi.fn()
}));
vi.mock("@/api/storage/view", () => ({ queryTimeSeriesRows: mocks.queryTimeSeries, searchRecordRows: vi.fn() }));
vi.mock("@/store/modules/space", () => ({
  useSpaceStore: () => ({ selectedSpaceId: "crypto", requireSpaceId: () => "crypto" })
}));
vi.mock("@arco-design/web-vue", () => ({ Message: { error: vi.fn(), warning: vi.fn() }, Modal: { confirm: vi.fn() } }));

import ViewBrowse from "./index.vue";

const columns = [
  { column_name: "open", value_type: "FIELD_VALUE_TYPE_DOUBLE" },
  { column_name: "close", value_type: "FIELD_VALUE_TYPE_DOUBLE" },
  { column_name: "rsi", value_type: "FIELD_VALUE_TYPE_DOUBLE", attributes: { column_kind: "output" } }
];

const rsiAbove70 = { fieldName: "rsi", operator: "gt" as const, value: "70", valueType: "FIELD_VALUE_TYPE_DOUBLE" as const };

const TableStub = defineComponent({
  props: { data: { type: Array, default: () => [] } },
  setup(props, { slots }) {
    provide("rows", toRef(props, "data"));
    return () => h("div", slots.columns?.());
  }
});

const ColumnStub = defineComponent({
  setup(_, { slots }) {
    const rows = inject<Ref<unknown[]>>("rows", ref([]));
    return () =>
      h(
        "div",
        rows.value.map((record, rowIndex) => slots.cell?.({ record, rowIndex }))
      );
  }
});

function mountBrowse(props: Record<string, unknown> = {}, slots: Record<string, unknown> = {}) {
  const stubNames = [
    "a-empty",
    "a-tabs",
    "a-tab-pane",
    "a-tag",
    "a-input",
    "a-dropdown",
    "a-doption",
    "a-tooltip",
    "a-modal",
    "a-descriptions",
    "a-descriptions-item",
    "icon-refresh",
    "icon-bar-chart",
    "icon-left",
    "icon-right",
    "KlineModal"
  ];
  return shallowMount(ViewBrowse, {
    props: { embedded: true, viewIds: ["view-factor"], activeViewId: "view-factor", ...props },
    slots,
    global: {
      stubs: {
        ...Object.fromEntries(stubNames.map(name => [name, true])),
        "a-button": { emits: ["click"], template: "<button @click=\"$emit('click')\"><slot /></button>" },
        "a-spin": { template: "<div><slot /></div>" },
        "a-alert": { template: "<div class='alert'><slot /></div>" },
        QueryControls: { template: "<div><slot /></div>" },
        ResultTable: { template: "<div><slot /></div>" },
        // 表格按行渲染每一列的 cell 插槽，便于断言 row-extra 拿到的行键。
        "a-table": TableStub,
        "a-table-column": ColumnStub
      }
    }
  });
}

function lastRequest() {
  return mocks.queryTimeSeries.mock.lastCall?.[0];
}

function clickButton(wrapper: ReturnType<typeof mountBrowse>, text: string) {
  const button = wrapper.findAll("button").find(item => item.text() === text);
  if (!button) throw new Error(`找不到按钮：${text}`);
  return button.trigger("click");
}

describe("ViewBrowse 扩展点", () => {
  beforeEach(() => {
    mocks.catalog.mockReset().mockResolvedValue({
      views: [{ view_id: "view-factor", dataset_id: "dataset-factor" }],
      datasets: [{ dataset_id: "dataset-factor", data_kind: "DATA_KIND_TIME_SERIES" }]
    });
    mocks.columns.mockReset().mockResolvedValue(columns);
    mocks.queryTimeSeries.mockReset().mockResolvedValue({
      rows: [
        {
          key: {
            space_id: "crypto",
            dataset_id: "dataset-factor",
            subject_id: "BTC",
            freq: "1m",
            series_tag: "",
            data_time: "t1"
          }
        }
      ]
    });
  });

  it("未传新参数时仍投影全部视图列（采集结果页的默认行为）", async () => {
    const wrapper = mountBrowse();
    await flushPromises();
    expect(mocks.queryTimeSeries).toHaveBeenCalledTimes(1);
    expect(lastRequest().column_names).toEqual(["open", "close", "rsi"]);
    expect(lastRequest().filter).toBeUndefined();
    wrapper.unmount();
  });

  it("queryReady 为 false 时不查询，放行后带着投影与附加条件查第一页", async () => {
    const wrapper = mountBrowse({ queryReady: false, projectColumns: ["close"], extraFilters: [rsiAbove70] });
    await flushPromises();
    expect(mocks.queryTimeSeries).not.toHaveBeenCalled();

    await wrapper.setProps({ queryReady: true });
    await flushPromises();
    expect(mocks.queryTimeSeries).toHaveBeenCalledTimes(1);
    expect(lastRequest().column_names).toEqual(["close"]);
    expect(lastRequest().page.page).toBe(1);
    const conds = lastRequest().filter.groups[0].conds;
    expect(conds).toContainEqual({ column: "rsi", op: "FILTER_OP_GT", values: [{ double_value: 70 }] });
    wrapper.unmount();
  });

  it("附加条件变化时回到第一页重查，投影变化时重查", async () => {
    const wrapper = mountBrowse({ projectColumns: ["close"] });
    await flushPromises();
    expect(mocks.queryTimeSeries).toHaveBeenCalledTimes(1);

    await wrapper.setProps({ extraFilters: [rsiAbove70] });
    await flushPromises();
    expect(mocks.queryTimeSeries).toHaveBeenCalledTimes(2);
    expect(lastRequest().filter.groups[0].conds).toHaveLength(1);

    await wrapper.setProps({ projectColumns: ["open", "close"] });
    await flushPromises();
    expect(mocks.queryTimeSeries).toHaveBeenCalledTimes(3);
    expect(lastRequest().column_names).toEqual(["open", "close"]);
    wrapper.unmount();
  });

  it("清空时先通知调用方，清空后的那次请求不带附加条件，且只查一次", async () => {
    // 模拟调用方：收到 cleared 后同步清掉自己的附加条件。
    const cleared = vi.fn(() => {
      void wrapper.setProps({ extraFilters: [] });
    });
    const wrapper = mountBrowse({ extraFilters: [rsiAbove70], onCleared: cleared });
    await flushPromises();
    expect(mocks.queryTimeSeries).toHaveBeenCalledTimes(1);

    await clickButton(wrapper, "清空");
    await flushPromises();
    expect(cleared).toHaveBeenCalledTimes(1);
    expect(mocks.queryTimeSeries).toHaveBeenCalledTimes(2);
    expect(lastRequest().filter).toBeUndefined();
    wrapper.unmount();
  });

  it("调用方没有附加条件时，清空照常重查一次", async () => {
    const wrapper = mountBrowse();
    await flushPromises();
    await clickButton(wrapper, "清空");
    await flushPromises();
    expect(mocks.queryTimeSeries).toHaveBeenCalledTimes(2);
    wrapper.unmount();
  });

  it("搜索栅格只为指定的列生成", async () => {
    const wrapper = mountBrowse({ searchColumns: ["close"] });
    await flushPromises();
    const labels = wrapper.findAll(".filter-item label").map(item => item.text());
    expect(labels.some(label => label.startsWith("close"))).toBe(true);
    expect(labels.some(label => label.startsWith("rsi"))).toBe(false);
    expect(labels.some(label => label.startsWith("数据ID"))).toBe(true);
    wrapper.unmount();
  });

  it("row-extra 插槽拿到原始完整行键（含空序列标签）", async () => {
    const seen: unknown[] = [];
    const wrapper = mountBrowse(
      {},
      {
        "row-extra": (slotProps: { rowKey: unknown }) => {
          seen.push(slotProps.rowKey);
          return h("span", "查看全部");
        }
      }
    );
    await flushPromises();
    expect(seen).toContainEqual({
      space_id: "crypto",
      dataset_id: "dataset-factor",
      subject_id: "BTC",
      freq: "1m",
      series_tag: "",
      data_time: "t1"
    });
    wrapper.unmount();
  });
});
