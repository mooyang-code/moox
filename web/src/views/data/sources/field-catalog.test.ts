import { describe, expect, it } from "vitest";
import type { ExtraFieldOption, OutputFieldOption } from "@/api/collector";
import type { Dataset, DatasetColumn } from "@/api/storage/types";
import {
  buildFieldGroupItems,
  fieldSourceSummary,
  fieldTabQueryFromRoute,
  fieldTabQueryToRoute,
  filterFieldCatalog,
  mapWithConcurrency,
  mergeFieldCatalog,
  UNGROUPED_LABEL
} from "./field-catalog";

const baseFields: OutputFieldOption[] = [
  { field_id: "open", display_name: "开盘价", group: "行情价格", value_type: "double" },
  { field_id: "close", display_name: "收盘价", group: "行情价格", value_type: "double" },
  { field_id: "volume", display_name: "成交量", group: "成交估值", value_type: "double" }
];

const extraFields: ExtraFieldOption[] = [
  {
    field_id: "funding_rate",
    display_name: "资金费率",
    group: "合约指标",
    market_id: "crypto",
    instrument_type: "swap",
    frequencies: ["1h", "1d"],
    provider_id: "binance",
    value_type: "double",
    fill: "zero"
  },
  {
    field_id: "adj_factor",
    display_name: "后复权因子",
    group: "复权",
    market_id: "stockcn",
    instrument_type: "equity",
    frequencies: ["1d"],
    provider_id: "sina_hfq",
    value_type: "double",
    fill: "prev"
  },
  {
    field_id: "fin_revenue",
    display_name: "营业收入",
    group: "财务数据",
    market_id: "stockcn",
    instrument_type: "equity",
    frequencies: ["1d"],
    provider_id: "sina_finance",
    value_type: "double",
    fill: "prev"
  }
];

const klineDataset: Dataset = {
  space_id: "stockcn",
  dataset_id: "dataset_cn_kline_1d",
  data_source_id: "tdx",
  name: "A 股日线",
  data_kind: "DATA_KIND_TIME_SERIES",
  freq: "1d",
  status: "active"
};

const financialRecordDataset: Dataset = {
  space_id: "stockcn",
  dataset_id: "dataset_stockcn_financial_summary",
  data_source_id: "quantclass",
  name: "财务摘要",
  data_kind: "DATA_KIND_RECORD",
  status: "active"
};

const klineColumns: DatasetColumn[] = [
  {
    space_id: "stockcn",
    dataset_id: "dataset_cn_kline_1d",
    column_name: "close",
    value_type: "FIELD_VALUE_TYPE_DOUBLE",
    display_name: "收盘价",
    group: "行情价格",
    status: "active"
  },
  {
    space_id: "stockcn",
    dataset_id: "dataset_cn_kline_1d",
    column_name: "adj_factor",
    value_type: "FIELD_VALUE_TYPE_DOUBLE",
    display_name: "后复权因子",
    group: "复权",
    status: "active",
    attributes: { fill: "prev" }
  }
];

const recordColumns: DatasetColumn[] = [
  {
    space_id: "stockcn",
    dataset_id: "dataset_stockcn_financial_summary",
    column_name: "report_date",
    value_type: "FIELD_VALUE_TYPE_TIME",
    display_name: "报告期",
    group: "财务数据",
    status: "active",
    required: true
  },
  {
    space_id: "stockcn",
    dataset_id: "dataset_stockcn_financial_summary",
    column_name: "custom_note",
    value_type: "FIELD_VALUE_TYPE_STRING",
    display_name: "备注",
    group: "",
    status: "disabled",
    aliases: ["memo"]
  }
];

describe("mergeFieldCatalog", () => {
  it("merges base, space-scoped extra, and non-factor dataset columns without duplicates", () => {
    const items = mergeFieldCatalog({
      spaceId: "stockcn",
      baseFields,
      extraFields,
      datasetBundles: [
        { dataset: klineDataset, columns: klineColumns },
        { dataset: financialRecordDataset, columns: recordColumns }
      ]
    });

    expect(items.map(i => i.field_id)).toEqual([
      "open",
      "close",
      "volume",
      "adj_factor",
      "fin_revenue",
      "report_date",
      "custom_note"
    ]);
    // 别的空间的附加字段不进入当前空间
    expect(items.find(i => i.field_id === "funding_rate")).toBeUndefined();

    const close = items.find(i => i.field_id === "close")!;
    expect(close.category).toBe("base");
    expect(close.datasets.map(d => d.dataset_id)).toEqual(["dataset_cn_kline_1d"]);

    const adj = items.find(i => i.field_id === "adj_factor")!;
    expect(adj.category).toBe("extra");
    expect(adj.value_type).toBe("double");
    expect(adj.fill).toBe("prev");
    expect(adj.extra_scopes[0].provider_name).toBe("复权因子（新浪）");
    expect(adj.datasets.map(d => d.dataset_id)).toEqual(["dataset_cn_kline_1d"]);

    const fin = items.find(i => i.field_id === "fin_revenue")!;
    expect(fin.category).toBe("extra");
    expect(fin.datasets).toEqual([]);

    const reportDate = items.find(i => i.field_id === "report_date")!;
    expect(reportDate.category).toBe("dataset");
    expect(reportDate.value_type).toBe("time");
    expect(reportDate.group).toBe("财务数据");
    expect(fieldSourceSummary(reportDate)).toBe("记录型 · 财务摘要");

    const note = items.find(i => i.field_id === "custom_note")!;
    expect(note.group).toBe(UNGROUPED_LABEL);
    expect(note.aliases).toEqual(["memo"]);
    expect(note.datasets[0].column_status).toBe("disabled");
  });

  it("orders groups by fixed business order with ungrouped last and computes filtered counts", () => {
    const items = mergeFieldCatalog({
      spaceId: "stockcn",
      baseFields,
      extraFields,
      datasetBundles: [{ dataset: financialRecordDataset, columns: recordColumns }]
    });

    const allGroups = buildFieldGroupItems(items, items);
    expect(allGroups).toEqual([
      { key: "行情价格", label: "行情价格", count: 2 },
      { key: "成交估值", label: "成交估值", count: 1 },
      { key: "复权", label: "复权", count: 1 },
      { key: "财务数据", label: "财务数据", count: 2 },
      { key: UNGROUPED_LABEL, label: UNGROUPED_LABEL, count: 1 }
    ]);

    const extraOnly = filterFieldCatalog(items, {
      group: "",
      keyword: "",
      category: "extra",
      valueType: "",
      landed: "",
      provider: ""
    });
    const filteredGroups = buildFieldGroupItems(items, extraOnly);
    expect(filteredGroups.find(g => g.key === "复权")?.count).toBe(1);
    expect(filteredGroups.find(g => g.key === "行情价格")?.count).toBe(0);
  });

  it("filters by provider, landed state, valueType, and keyword across aliases/datasets/providers", () => {
    const items = mergeFieldCatalog({
      spaceId: "stockcn",
      baseFields,
      extraFields,
      datasetBundles: [
        { dataset: klineDataset, columns: klineColumns },
        { dataset: financialRecordDataset, columns: recordColumns }
      ]
    });
    const baseFilter = {
      group: "",
      keyword: "",
      category: "" as const,
      valueType: "" as const,
      landed: "" as const,
      provider: ""
    };

    expect(filterFieldCatalog(items, { ...baseFilter, provider: "sina_finance" }).map(i => i.field_id)).toEqual(["fin_revenue"]);
    expect(filterFieldCatalog(items, { ...baseFilter, landed: "spec_only" }).map(i => i.field_id)).toEqual([
      "open",
      "volume",
      "fin_revenue"
    ]);
    expect(filterFieldCatalog(items, { ...baseFilter, keyword: "memo" }).map(i => i.field_id)).toEqual(["custom_note"]);
    expect(filterFieldCatalog(items, { ...baseFilter, keyword: "财务摘要" }).map(i => i.field_id)).toEqual([
      "report_date",
      "custom_note"
    ]);
    expect(filterFieldCatalog(items, { ...baseFilter, valueType: "time" }).map(i => i.field_id)).toEqual(["report_date"]);
  });

  it("round-trips route query parameters", () => {
    const parsed = fieldTabQueryFromRoute({
      group: "财务数据",
      keyword: "revenue",
      category: "extra",
      value_type: "double",
      landed: "spec_only",
      provider: "sina_finance"
    });
    expect(parsed).toEqual({
      group: "财务数据",
      keyword: "revenue",
      category: "extra",
      valueType: "double",
      landed: "spec_only",
      provider: "sina_finance"
    });
    expect(fieldTabQueryToRoute(parsed)).toEqual({
      group: "财务数据",
      keyword: "revenue",
      category: "extra",
      value_type: "double",
      landed: "spec_only",
      provider: "sina_finance"
    });
  });

  it("runs mapWithConcurrency preserving order and capturing partial failures", async () => {
    const results = await mapWithConcurrency([1, 2, 3, 4], 2, async n => {
      if (n === 3) throw new Error("boom");
      return n * 10;
    });
    expect(results[0]).toEqual({ status: "fulfilled", value: 10 });
    expect(results[1]).toEqual({ status: "fulfilled", value: 20 });
    expect(results[2].status).toBe("rejected");
    expect(results[3]).toEqual({ status: "fulfilled", value: 40 });
  });

  it("prefers first active S3 column metadata over earlier disabled column and dedupes providers case-insensitively", () => {
    const items = mergeFieldCatalog({
      spaceId: "stockcn",
      extraFields: [
        {
          field_id: "adj_factor",
          display_name: "后复权因子",
          group: "复权",
          market_id: "stockcn",
          instrument_type: "equity",
          frequencies: ["1d"],
          provider_id: "Sina_HFQ",
          value_type: "double",
          fill: "prev"
        },
        {
          field_id: "adj_factor",
          display_name: "后复权因子",
          group: "复权",
          market_id: "stockcn",
          instrument_type: "equity",
          frequencies: ["1w"],
          provider_id: "sina_hfq",
          value_type: "double",
          fill: "prev"
        }
      ],
      datasetBundles: [
        {
          dataset: { ...financialRecordDataset, dataset_id: "ds_old", name: "旧财报" },
          columns: [
            {
              space_id: "stockcn",
              dataset_id: "ds_old",
              column_name: "net_margin",
              value_type: "FIELD_VALUE_TYPE_STRING",
              display_name: "旧净利率",
              group: "旧分组",
              status: "disabled",
              attributes: { fill: "zero" }
            }
          ]
        },
        {
          dataset: { ...financialRecordDataset, dataset_id: "ds_new", name: "新财报" },
          columns: [
            {
              space_id: "stockcn",
              dataset_id: "ds_new",
              column_name: "net_margin",
              value_type: "FIELD_VALUE_TYPE_DOUBLE",
              display_name: "销售净利率",
              group: "财务数据",
              status: "active",
              attributes: { fill: "prev" }
            }
          ]
        }
      ]
    });

    const adj = items.find(i => i.field_id === "adj_factor")!;
    expect(adj.extra_scopes).toHaveLength(1);
    expect(adj.extra_scopes[0].provider_name).toBe("复权因子（新浪）");
    expect(adj.extra_scopes[0].frequencies).toEqual(["1d", "1w"]);

    const margin = items.find(i => i.field_id === "net_margin")!;
    expect(margin.display_name).toBe("销售净利率");
    expect(margin.group).toBe("财务数据");
    expect(margin.value_type).toBe("double");
    expect(margin.fill).toBe("prev");
    expect(margin.datasets.map(d => d.dataset_id)).toEqual(["ds_old", "ds_new"]);

    // 前 disabled 有名有组 + 后 active 为空名空组时，不把有效元信息覆盖退化为空
    const preserved = mergeFieldCatalog({
      spaceId: "stockcn",
      datasetBundles: [
        {
          dataset: { ...financialRecordDataset, dataset_id: "ds_a", name: "旧表" },
          columns: [
            {
              space_id: "stockcn",
              dataset_id: "ds_a",
              column_name: "roe",
              value_type: "FIELD_VALUE_TYPE_DOUBLE",
              display_name: "净资产收益率",
              group: "财务数据",
              status: "disabled"
            }
          ]
        },
        {
          dataset: { ...financialRecordDataset, dataset_id: "ds_b", name: "新表" },
          columns: [
            {
              space_id: "stockcn",
              dataset_id: "ds_b",
              column_name: "roe",
              value_type: "FIELD_VALUE_TYPE_DOUBLE",
              display_name: "",
              group: "",
              status: "active"
            }
          ]
        }
      ]
    });
    const roe = preserved.find(i => i.field_id === "roe")!;
    expect(roe.display_name).toBe("净资产收益率");
    expect(roe.group).toBe("财务数据");
  });
});
