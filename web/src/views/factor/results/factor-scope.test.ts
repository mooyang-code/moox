import { describe, expect, it } from "vitest";
import type { FactorCategory, FactorMember } from "@/api/factor/types";
import type { ViewColumn } from "@/api/storage/types";
import {
  buildFactorScope,
  countByCategory,
  filterOutputs,
  formatValue,
  pageSlice,
  paramText,
  rowValuesRequest,
  valueColumnsFor
} from "./factor-scope";
import { normalizeColumnSettings, tableColumns } from "./column-settings";

const col = (name: string, attributes?: Record<string, string>): ViewColumn => ({
  space_id: "crypto",
  view_id: "v",
  column_name: name,
  origin_type: "COLUMN_ORIGIN_TYPE_DATASET_COLUMN",
  origin_id: name,
  value_type: "FIELD_VALUE_TYPE_DOUBLE",
  attributes
});

function member(alias: string, factorId: string, category: string, outputs: string[], params: string[] = []): FactorMember {
  return {
    set_id: "s",
    member_alias: alias,
    factor_id: factorId,
    status: "enabled",
    factor: {
      factor_id: factorId,
      name: `${factorId} 名称`,
      spec_hash: "",
      factor_type: "timeseries",
      input_columns: ["close"],
      outputs,
      lookback_periods: 1,
      allow_partial_universe: false,
      category_id: category,
      created_at: "",
      updated_at: ""
    },
    created_at: "",
    updated_at: "",
    output_columns: outputs.map(output => `${alias}_${output}`),
    param_columns: params.map(name => `${alias}__${name}`)
  };
}

const categories: FactorCategory[] = [
  { category_id: "trend", name: "趋势类" },
  { category_id: "momentum", name: "动量类" }
];

const factorAttrs = (alias: string, kind: "output" | "param", extra: Record<string, string> = {}) => ({
  member_alias: alias,
  column_kind: kind,
  category_id: "stale",
  ...extra
});

function fixture() {
  const members = [
    member("rsi_fast", "Rsi", "momentum", ["rsi"], ["window"]),
    member("rsi_slow", "Rsi", "momentum", ["rsi"], ["window"]),
    member("boll", "Boll", "trend", ["mid", "up"], ["window", "k"])
  ];
  const viewColumns = [
    col("subject_id"),
    col("open"),
    col("close"),
    col("request_id"),
    col("quality_status"),
    ...members.flatMap(m => [
      ...(m.output_columns ?? []).map(c => col(c, factorAttrs(m.member_alias, "output"))),
      ...(m.param_columns ?? []).map(c => col(c, factorAttrs(m.member_alias, "param", { param_name: c.split("__")[1] })))
    ]),
    // 已移除成员的残留列：仍在视图里，带因子属性
    col("gone_rsi", factorAttrs("gone", "output")),
    col("gone__window", factorAttrs("gone", "param"))
  ];
  return { members, viewColumns };
}

describe("buildFactorScope", () => {
  it("以成员清单建目录：同一定义多别名各自成项，分类按清单顺序，残留列不进目录也不进行情列", () => {
    const { members, viewColumns } = fixture();
    const scope = buildFactorScope({ viewColumns, members, categories });
    expect(scope.outputs.map(item => item.column)).toEqual(["boll_mid", "boll_up", "rsi_fast_rsi", "rsi_slow_rsi"]);
    expect(scope.outputs[2]).toMatchObject({ alias: "rsi_fast", factorId: "Rsi", output: "rsi", categoryId: "momentum" });
    expect(scope.categoryOrder).toEqual(["trend", "momentum"]);
    expect(scope.baseMarket).toEqual(["open", "close"]);
    expect(scope.otherSource).toEqual(["request_id", "quality_status"]);
    expect(scope.outputByColumn.has("gone_rsi")).toBe(false);
    expect([...scope.baseMarket, ...scope.otherSource]).not.toContain("gone__window");
  });

  it("分类以成员当前定义为准，不看列属性里可能过期的分类", () => {
    const { members, viewColumns } = fixture();
    const scope = buildFactorScope({
      viewColumns,
      members,
      categories,
      hidden: { categories: ["stale"], members: [], columns: [] }
    });
    expect(scope.outputs).toHaveLength(4);
    const hiddenMomentum = buildFactorScope({
      viewColumns,
      members,
      categories,
      hidden: { categories: ["momentum"], members: [], columns: [] }
    });
    expect(hiddenMomentum.outputs.map(item => item.alias)).toEqual(["boll", "boll"]);
    expect(hiddenMomentum.paramsByAlias.has("rsi_fast")).toBe(false);
  });

  it("策略可按成员、单个输出、单个参数、行情列隐藏", () => {
    const { members, viewColumns } = fixture();
    const scope = buildFactorScope({
      viewColumns,
      members,
      categories,
      hidden: { categories: [], members: ["rsi_slow"], columns: ["boll_up", "boll__k", "open"] }
    });
    expect(scope.outputs.map(item => item.column)).toEqual(["boll_mid", "rsi_fast_rsi"]);
    expect(scope.paramsByAlias.get("boll")).toEqual([{ column: "boll__window", name: "window" }]);
    expect(scope.baseMarket).toEqual(["close"]);
  });

  it("视图里还没有的列不进目录；没有分类的定义归入未分类并排在最后", () => {
    const { viewColumns } = fixture();
    const members = [member("x", "X", "", ["a"]), member("boll", "Boll", "trend", ["mid", "missing"])];
    const scope = buildFactorScope({ viewColumns: [...viewColumns, col("x_a")], members, categories });
    expect(scope.outputs.map(item => item.column)).toEqual(["boll_mid", "x_a"]);
    expect(scope.categoryOrder).toEqual(["trend", "uncategorized"]);
    expect(scope.categoryNames.get("uncategorized")).toBe("未分类");
  });
});

describe("目录过滤、分页与取值", () => {
  const { members, viewColumns } = fixture();
  const scope = buildFactorScope({ viewColumns, members, categories });

  it("按分类与关键词过滤并统计数量", () => {
    expect(filterOutputs(scope.outputs, { category: "momentum" }).map(item => item.alias)).toEqual(["rsi_fast", "rsi_slow"]);
    expect(filterOutputs(scope.outputs, { keyword: "BOLL 名" }).map(item => item.column)).toEqual(["boll_mid", "boll_up"]);
    expect(countByCategory(scope.outputs)).toEqual(
      new Map([
        ["trend", 2],
        ["momentum", 2]
      ])
    );
  });

  it("一页的取值列是这页的输出加所属成员的参数列，多输出成员跨页时每页都带参数", () => {
    const page1 = pageSlice(scope.outputs, 1, 1);
    const page2 = pageSlice(scope.outputs, 2, 1);
    expect(valueColumnsFor(scope, page1)).toEqual(["boll_mid", "boll__window", "boll__k"]);
    expect(valueColumnsFor(scope, page2)).toEqual(["boll_up", "boll__window", "boll__k"]);
    expect(valueColumnsFor(scope, scope.outputs.slice(0, 2))).toEqual(["boll_mid", "boll__window", "boll__k", "boll_up"]);
  });

  it("单行请求带显式空 series_tag、[t, t+1ms) 和 total_mode NONE", () => {
    const req = rowValuesRequest(
      "v",
      {
        space_id: "crypto",
        dataset_id: "d",
        subject_id: "BTC",
        freq: "1m",
        series_tag: undefined,
        data_time: "2026-10-24T10:00:00Z"
      },
      ["boll_mid"]
    );
    expect(req.selectors).toEqual([{ space_id: "crypto", dataset_id: "d", subject_id: "BTC", freq: "1m", series_tag: "" }]);
    expect(req.time_range).toEqual({ start_time: "2026-10-24T10:00:00.000Z", end_time: "2026-10-24T10:00:00.001Z" });
    expect(req).toMatchObject({ column_names: ["boll_mid"], page: { page: 1, size: 1 }, total_mode: "NONE" });
  });

  it("参数文本与数值格式", () => {
    expect(paramText(scope, "boll", { boll__window: "20", boll__k: "2" })).toBe("window=20, k=2");
    expect(paramText(scope, "nobody", {})).toBe("");
    expect(formatValue("0.123456789")).toBe("0.123457");
    expect(formatValue("70")).toBe("70");
    expect(formatValue("")).toBe("-");
    expect(formatValue(undefined)).toBe("-");
  });
});

describe("几千个因子的规模", () => {
  it("2000 个成员、8000 列时，搜索项、显示列与投影只含行情列和钉住列", () => {
    const members = Array.from({ length: 2000 }, (_, index) =>
      member(`m${index}`, `F${index}`, index % 2 ? "trend" : "momentum", ["a", "b"], ["window", "k"])
    );
    const viewColumns = [
      col("open"),
      col("close"),
      col("volume"),
      ...members.flatMap(m => [...(m.output_columns ?? []), ...(m.param_columns ?? [])].map(c => col(c, { column_kind: "x" })))
    ];
    expect(viewColumns.length).toBe(8003);
    const scope = buildFactorScope({ viewColumns, members, categories });
    expect(scope.outputs).toHaveLength(4000);
    expect(scope.baseMarket).toEqual(["open", "close", "volume"]);

    const settings = normalizeColumnSettings(
      { hiddenSystem: [], market: ["open", "close", "volume"], pinned: ["m7_a", "m9_b"] },
      scope
    );
    const table = tableColumns(settings, scope);
    expect(table.search).toEqual(["open", "close", "volume"]);
    expect(table.display.business).toEqual(["open", "close", "volume", "m7_a", "m9_b"]);
    expect(table.project).toEqual(table.display.business);
    expect(filterOutputs(scope.outputs, { category: "trend" })).toHaveLength(2000);
    expect(valueColumnsFor(scope, pageSlice(scope.outputs, 3)).length).toBeLessThanOrEqual(60);
  });
});
