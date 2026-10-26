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

/** 单输出因子的列名就是成员键，多输出因子是 成员键__输出名（与 utils/factor-name 的规则一致）。 */
function member(
  key: string,
  factorId: string,
  category: string,
  outputs: string[],
  kind: FactorMember["kind"] = "fixed"
): FactorMember {
  return {
    task_id: "s",
    member_key: key,
    kind,
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
    output_columns: outputs.length === 1 ? [key] : outputs.map(output => `${key}__${output}`)
  };
}

const categories: FactorCategory[] = [
  { category_id: "trend", name: "趋势类" },
  { category_id: "momentum", name: "动量类" }
];

const outputAttrs = (key: string) => ({ column_kind: "output", member_key: key, category_id: "stale" });

function fixture() {
  const members = [
    member("rsi__14", "rsi", "momentum", ["rsi"]),
    member("rsi__var", "rsi", "momentum", ["rsi"], "var"),
    member("boll", "boll", "trend", ["mid", "up"])
  ];
  const viewColumns = [
    col("subject_id"),
    col("open"),
    col("close"),
    col("request_id"),
    col("quality_status"),
    ...members.flatMap(m => (m.output_columns ?? []).map(c => col(c, outputAttrs(m.member_key)))),
    // 系统列：各成员这一期实际用的参数，不是因子值列。
    col("__params", { column_kind: "params" }),
    // 已移除成员的残留列：仍在视图里，带因子属性
    col("gone", outputAttrs("gone"))
  ];
  return { members, viewColumns };
}

describe("buildFactorScope", () => {
  it("以成员清单建目录：同一定义的固定与可变成员各自成项，分类按清单顺序，残留列和 __params 不进目录也不进行情列", () => {
    const { members, viewColumns } = fixture();
    const scope = buildFactorScope({ viewColumns, members, categories });
    expect(scope.outputs.map(item => item.column)).toEqual(["boll__mid", "boll__up", "rsi__14", "rsi__var"]);
    expect(scope.outputs[2]).toMatchObject({ memberKey: "rsi__14", factorId: "rsi", output: "rsi", categoryId: "momentum" });
    expect(scope.categoryOrder).toEqual(["trend", "momentum"]);
    expect(scope.baseMarket).toEqual(["open", "close"]);
    expect(scope.otherSource).toEqual(["request_id", "quality_status"]);
    expect(scope.outputByColumn.has("gone")).toBe(false);
    expect([...scope.baseMarket, ...scope.otherSource]).not.toContain("__params");
    expect(scope.paramsColumn).toBe("__params");
  });

  it("视图没有 __params 列时 paramsColumn 为空", () => {
    const { members, viewColumns } = fixture();
    const scope = buildFactorScope({
      viewColumns: viewColumns.filter(item => item.column_name !== "__params"),
      members,
      categories
    });
    expect(scope.paramsColumn).toBe("");
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
    expect(hiddenMomentum.outputs.map(item => item.memberKey)).toEqual(["boll", "boll"]);
  });

  it("策略可按成员键、单个输出、行情列隐藏", () => {
    const { members, viewColumns } = fixture();
    const scope = buildFactorScope({
      viewColumns,
      members,
      categories,
      hidden: { categories: [], members: ["rsi__var"], columns: ["boll__up", "open"] }
    });
    expect(scope.outputs.map(item => item.column)).toEqual(["boll__mid", "rsi__14"]);
    expect(scope.baseMarket).toEqual(["close"]);
  });

  it("视图里还没有的列不进目录；没有分类的定义归入未分类并排在最后", () => {
    const { viewColumns } = fixture();
    const members = [member("x", "x", "", ["x"]), member("boll", "boll", "trend", ["mid", "missing"])];
    const scope = buildFactorScope({ viewColumns: [...viewColumns, col("x")], members, categories });
    expect(scope.outputs.map(item => item.column)).toEqual(["boll__mid", "x"]);
    expect(scope.categoryOrder).toEqual(["trend", "uncategorized"]);
    expect(scope.categoryNames.get("uncategorized")).toBe("未分类");
  });
});

describe("目录过滤、分页与取值", () => {
  const { members, viewColumns } = fixture();
  const scope = buildFactorScope({ viewColumns, members, categories });

  it("按分类与关键词过滤并统计数量", () => {
    expect(filterOutputs(scope.outputs, { category: "momentum" }).map(item => item.memberKey)).toEqual(["rsi__14", "rsi__var"]);
    expect(filterOutputs(scope.outputs, { keyword: "BOLL 名" }).map(item => item.column)).toEqual(["boll__mid", "boll__up"]);
    expect(filterOutputs(scope.outputs, { keyword: "__var" }).map(item => item.column)).toEqual(["rsi__var"]);
    expect(countByCategory(scope.outputs)).toEqual(
      new Map([
        ["trend", 2],
        ["momentum", 2]
      ])
    );
  });

  it("一页的取值列是这页的输出；打开显示参数时再加 __params，只加一次", () => {
    const page1 = pageSlice(scope.outputs, 1, 1);
    expect(valueColumnsFor(scope, page1)).toEqual(["boll__mid"]);
    expect(valueColumnsFor(scope, page1, true)).toEqual(["boll__mid", "__params"]);
    expect(valueColumnsFor(scope, scope.outputs.slice(0, 3), true)).toEqual(["boll__mid", "boll__up", "rsi__14", "__params"]);
    const noParams = buildFactorScope({
      viewColumns: viewColumns.filter(item => item.column_name !== "__params"),
      members,
      categories
    });
    expect(valueColumnsFor(noParams, page1, true)).toEqual(["boll__mid"]);
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
      ["boll__mid"]
    );
    expect(req.selectors).toEqual([{ space_id: "crypto", dataset_id: "d", subject_id: "BTC", freq: "1m", series_tag: "" }]);
    expect(req.time_range).toEqual({ start_time: "2026-10-24T10:00:00.000Z", end_time: "2026-10-24T10:00:00.001Z" });
    expect(req).toMatchObject({ column_names: ["boll__mid"], page: { page: 1, size: 1 }, total_mode: "NONE" });
  });

  it("成员在这一行实际用的参数取自 __params，数值格式", () => {
    const raw = '{"boll":{"k":2,"window":20},"rsi__var":{"window":14.5}}';
    expect(paramText("boll", raw)).toBe("k=2, window=20");
    expect(paramText("rsi__var", raw)).toBe("window=14.5");
    // 这一行没有记录这个成员（按默认参数计算）、没有读到 __params 或内容不合法时都为空。
    expect(paramText("rsi__14", raw)).toBe("");
    expect(paramText("boll", undefined)).toBe("");
    expect(paramText("boll", "not json")).toBe("");
    expect(formatValue("0.123456789")).toBe("0.123457");
    expect(formatValue("70")).toBe("70");
    expect(formatValue("")).toBe("-");
    expect(formatValue(undefined)).toBe("-");
  });
});

describe("几千个因子的规模", () => {
  it("2000 个成员、4000 列时，搜索项、显示列与投影只含行情列和钉住列", () => {
    const members = Array.from({ length: 2000 }, (_, index) =>
      member(`m${index}`, `f${index}`, index % 2 ? "trend" : "momentum", ["a", "b"])
    );
    const viewColumns = [
      col("open"),
      col("close"),
      col("volume"),
      ...members.flatMap(m => (m.output_columns ?? []).map(c => col(c, { column_kind: "output" })))
    ];
    expect(viewColumns.length).toBe(4003);
    const scope = buildFactorScope({ viewColumns, members, categories });
    expect(scope.outputs).toHaveLength(4000);
    expect(scope.baseMarket).toEqual(["open", "close", "volume"]);

    const settings = normalizeColumnSettings(
      { hiddenSystem: [], market: ["open", "close", "volume"], pinned: ["m7__a", "m9__b"] },
      scope
    );
    const table = tableColumns(settings, scope);
    expect(table.search).toEqual(["open", "close", "volume"]);
    expect(table.display.business).toEqual(["open", "close", "volume", "m7__a", "m9__b"]);
    expect(table.project).toEqual(table.display.business);
    expect(filterOutputs(scope.outputs, { category: "trend" })).toHaveLength(2000);
    expect(valueColumnsFor(scope, pageSlice(scope.outputs, 3)).length).toBeLessThanOrEqual(60);
  });
});
