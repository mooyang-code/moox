import { describe, expect, it } from "vitest";
import { UNCATEGORIZED, type FactorCategory, type FactorDef, type FactorTaskInfo } from "@/api/factor/types";
import {
  buildOutputFieldCategoryItems,
  expandFactorOutputFields,
  filterFactorOutputFields,
  outputFieldsQueryFromRoute,
  outputFieldsQueryToRoute
} from "./output-fields-model";

const makeFactor = (id: string, name: string, outputs: string[], categoryId?: string): FactorDef => ({
  factor_id: id,
  name,
  spec_hash: "h",
  factor_type: id === "rank_ret" ? "cross_section" : "timeseries",
  input_columns: ["close"],
  outputs,
  lookback_periods: 20,
  allow_partial_universe: false,
  category_id: categoryId,
  created_at: "0",
  updated_at: "0"
});

const categories: FactorCategory[] = [
  { category_id: "trend", name: "趋势类", sort_order: 1 },
  { category_id: "volatility", name: "波动率", sort_order: 2 },
  { category_id: "empty", name: "空分类", sort_order: 3 }
];

const tasks: FactorTaskInfo[] = [
  {
    factor_task: {
      task_id: "ftask_swap_1m",
      space_id: "crypto",
      source_dataset_id: "ds_swap_1m",
      freq: "1m",
      name: "合约一分钟",
      result_dataset_id: "dataset_factor_swap_1m",
      status: "enabled"
    },
    members: [
      {
        task_id: "ftask_swap_1m",
        factor_id: "ma",
        status: "enabled",
        factor: makeFactor("ma", "均线", ["ma"], "trend"),
        created_at: "0",
        updated_at: "0",
        member_key: "ma__5",
        output_columns: ["ma__5"],
        params: { window: 5 },
        kind: "fixed"
      },
      {
        task_id: "ftask_swap_1m",
        factor_id: "boll",
        status: "enabled",
        factor: makeFactor("boll", "布林带", ["mid", "up", "low"], "volatility"),
        created_at: "0",
        updated_at: "0",
        member_key: "boll__var",
        output_columns: ["boll__var__mid", "boll__var__up", "boll__var__low"],
        params: { window: 20 },
        kind: "var"
      }
    ]
  },
  {
    factor_task: {
      task_id: "ftask_spot_1m",
      space_id: "crypto",
      source_dataset_id: "ds_spot_1m",
      freq: "1m",
      name: "现货一分钟",
      result_dataset_id: "dataset_factor_spot_1m",
      status: "enabled"
    },
    members: [
      {
        task_id: "ftask_spot_1m",
        factor_id: "rank_ret",
        status: "disabled",
        factor: makeFactor("rank_ret", "收益排名", ["rank_ret"]),
        created_at: "0",
        updated_at: "0",
        member_key: "rank_ret",
        output_columns: [],
        kind: "fixed"
      }
    ]
  }
];

describe("output-fields-model", () => {
  it("expands single-output, multi-output, and fallback output columns from tasks", () => {
    const rows = expandFactorOutputFields(tasks, categories, t => `${t.name} · ${t.freq}`);
    expect(rows.map(r => r.column_name)).toEqual(["ma__5", "boll__var__mid", "boll__var__up", "boll__var__low", "rank_ret"]);
    expect(rows[0]).toMatchObject({
      factor_id: "ma",
      category_id: "trend",
      category_name: "趋势类",
      member_kind: "fixed",
      params_text: "window=5",
      output_label: ""
    });
    expect(rows[1]).toMatchObject({
      factor_id: "boll",
      output_label: "mid",
      member_kind: "var",
      category_id: "volatility"
    });
    // output_columns 为空时回退 factor.outputs，无分类归入未分类
    expect(rows[4]).toMatchObject({
      column_name: "rank_ret",
      category_id: UNCATEGORIZED,
      category_name: "未分类",
      member_status: "disabled"
    });
  });

  it("builds category sidebar items skipping empty categories and tracking filtered counts", () => {
    const rows = expandFactorOutputFields(tasks, categories, t => `${t.name} · ${t.freq}`);
    const filtered = filterFactorOutputFields(rows, {
      category: "",
      taskId: "ftask_swap_1m",
      kind: "",
      status: "",
      keyword: ""
    });
    expect(buildOutputFieldCategoryItems(categories, rows, filtered)).toEqual([
      { key: "trend", label: "趋势类", count: 1 },
      { key: "volatility", label: "波动率", count: 3 },
      { key: UNCATEGORIZED, label: "未分类", count: 0 }
    ]);
  });

  it("filters by task, kind, status, category, and keyword", () => {
    const rows = expandFactorOutputFields(tasks, categories, t => `${t.name} · ${t.freq}`);
    const empty = { category: "", taskId: "", kind: "" as const, status: "" as const, keyword: "" };
    expect(filterFactorOutputFields(rows, { ...empty, kind: "var" }).length).toBe(3);
    expect(filterFactorOutputFields(rows, { ...empty, status: "disabled" }).map(r => r.column_name)).toEqual(["rank_ret"]);
    expect(filterFactorOutputFields(rows, { ...empty, keyword: "布林带" }).length).toBe(3);
    expect(filterFactorOutputFields(rows, { ...empty, keyword: "boll__var__up" }).map(r => r.column_name)).toEqual([
      "boll__var__up"
    ]);
  });

  it("round-trips route query parameters", () => {
    const parsed = outputFieldsQueryFromRoute({
      category: "trend",
      task: "ftask_swap_1m",
      kind: "var",
      status: "enabled",
      keyword: "boll"
    });
    expect(parsed).toEqual({
      category: "trend",
      taskId: "ftask_swap_1m",
      kind: "var",
      status: "enabled",
      keyword: "boll"
    });
    expect(outputFieldsQueryToRoute(parsed)).toEqual({
      category: "trend",
      task: "ftask_swap_1m",
      kind: "var",
      status: "enabled",
      keyword: "boll"
    });
  });
});
