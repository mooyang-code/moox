import fs from "node:fs";
import path from "node:path";
import { describe, expect, it, vi } from "vitest";
import type { FactorDef, FactorInfo, FactorUsage } from "@/api/factor/types";
import {
  categoryCounts,
  definitionRows,
  deleteCategoryMovingFactors,
  moveFactorsToCategory,
  definitionDeleteState,
  definitionEditState,
  filterDefinitions,
  orderedCategories,
  usageChips,
  usageCounts
} from "./definitions-model";

const def = (id: string, patch: Partial<FactorDef> = {}): FactorDef => ({
  factor_id: id,
  name: `${id}_module`,
  factor_type: "timeseries",
  spec_hash: "h",
  input_columns: ["close"],
  outputs: [id.toLowerCase()],
  lookback_periods: 20,
  allow_partial_universe: false,
  created_at: "",
  updated_at: "",
  ...patch
});

const info = (id: string, usages: FactorUsage[] = [], patch: Partial<FactorDef> = {}): FactorInfo => ({
  factor: def(id, patch),
  usages
});

const items: FactorInfo[] = [
  info("Bias", [{ task_id: "task_a", status: "enabled" }]),
  info("Momentum", [{ task_id: "task_b", status: "disabled" }], { factor_type: "cross_section" }),
  info("Idle")
];

describe("definitions model", () => {
  it("usage filter: all/using/idle counts", () => {
    expect(usageCounts(items)).toEqual({ all: 3, using: 2, idle: 1 });
    const ids = (usage: "all" | "using" | "idle") =>
      filterDefinitions(items, { type: "", usage, keyword: "" }).map(item => item.factor.factor_id);
    expect(ids("all")).toEqual(["Bias", "Momentum", "Idle"]);
    expect(ids("using")).toEqual(["Bias", "Momentum"]);
    expect(ids("idle")).toEqual(["Idle"]);
  });

  it("type filter", () => {
    const ids = filterDefinitions(items, { type: "cross_section", usage: "all", keyword: "" }).map(item => item.factor.factor_id);
    expect(ids).toEqual(["Momentum"]);
  });

  it("search by factor_id or module name", () => {
    const byId = filterDefinitions(items, { type: "", usage: "all", keyword: "bias" });
    expect(byId.map(item => item.factor.factor_id)).toEqual(["Bias"]);
    const byModule = filterDefinitions(items, { type: "", usage: "all", keyword: "idle_mod" });
    expect(byModule.map(item => item.factor.factor_id)).toEqual(["Idle"]);
  });

  it("usage chips carry dataset name · freq and status dot", () => {
    const labels: Record<string, string> = { task_a: "现货K线 · 1m" };
    const chips = usageChips(
      [
        { task_id: "task_a", member_key: "bias", status: "enabled" },
        { task_id: "task_gone", member_key: "bias__var", status: "disabled" }
      ],
      taskId => labels[taskId] ?? taskId
    );
    expect(chips).toEqual([
      {
        key: "task_a/bias",
        taskId: "task_a",
        memberKey: "bias",
        label: "现货K线 · 1m",
        status: "enabled",
        statusLabel: "已启用",
        paramsText: undefined
      },
      {
        key: "task_gone/bias__var",
        taskId: "task_gone",
        memberKey: "bias__var",
        label: "task_gone",
        status: "disabled",
        statusLabel: "已停用",
        paramsText: undefined
      }
    ]);
  });

  it("usage chips show the fixed params of each member", () => {
    const chips = usageChips(
      [
        { task_id: "task_a", member_key: "ema", status: "enabled", params: { span: 20, warmup: 0 } },
        { task_id: "task_a", member_key: "ema__20_50000", status: "enabled", params: { warmup: 50000, span: 20 } }
      ],
      () => "5m 因子任务"
    );
    expect(chips.map(chip => chip.paramsText)).toEqual(["span=20, warmup=0", "span=20, warmup=50000"]);
    expect(chips.map(chip => chip.key)).toEqual(["task_a/ema", "task_a/ema__20_50000"]);
  });

  it("edit locked when any enabled usage, with reason", () => {
    const locked = definitionEditState(items[0]);
    expect(locked.disabled).toBe(true);
    expect(locked.reason).toContain("需先停用");
    expect(definitionEditState(items[1]).disabled).toBe(false);
  });

  it("delete locked when any usage, with reason", () => {
    expect(definitionDeleteState(items[0]).disabled).toBe(true);
    expect(definitionDeleteState(items[1])).toMatchObject({ disabled: true });
    expect(definitionDeleteState(items[1]).reason).toContain("从计算任务中移除");
  });

  it("unused definition can be edited and deleted", () => {
    expect(definitionEditState(items[2])).toEqual({ disabled: false, reason: "" });
    expect(definitionDeleteState(items[2])).toEqual({ disabled: false, reason: "" });
  });
});

describe("definitions page contract", () => {
  const page = fs.readFileSync(path.resolve(__dirname, "index.vue"), "utf8");
  const table = fs.readFileSync(path.resolve(__dirname, "definition-table.vue"), "utf8");

  it("definitions page has no enable/disable action and no status column", () => {
    for (const source of [page, table]) {
      expect(source).not.toContain("setFactorMemberStatus");
      expect(source).not.toContain('title="状态"');
      expect(source).not.toContain('title="最近周期"');
    }
    expect(table).toContain('title="使用情况"');
    expect(table).toContain("未被使用");
  });

  it("reads definitions without the spec and fetches it only for the detail view", () => {
    expect(page).toContain("listFactors");
    expect(page).not.toContain("include_spec");
    expect(fs.readFileSync(path.resolve(__dirname, "factor-detail-drawer.vue"), "utf8")).toContain("getFactor");
  });
});

describe("分类树", () => {
  const info = (id: string, category?: string): FactorInfo => ({ factor: def(id, { category_id: category }), usages: [] });

  it("按 sort_order 排序，未分类固定最后（清单里没有时补上）", () => {
    expect(
      orderedCategories([
        { category_id: "uncategorized", name: "未分类", sort_order: 0 },
        { category_id: "trend", name: "趋势类", sort_order: 2 },
        { category_id: "momentum", name: "动量类", sort_order: 1 }
      ]).map(item => item.category_id)
    ).toEqual(["momentum", "trend", "uncategorized"]);
    expect(orderedCategories([]).map(item => item.category_id)).toEqual(["uncategorized"]);
  });

  it("按过滤后的定义计数，没有分类的算未分类", () => {
    const items = [info("A", "trend"), info("B", "trend"), info("C")];
    const filtered = filterDefinitions(items, { type: "", usage: "all", keyword: "A" });
    expect(categoryCounts(items)).toEqual({ trend: 2, uncategorized: 1 });
    expect(categoryCounts(filtered)).toEqual({ trend: 1 });
  });

  it("删除非空分类：逐个迁到未分类后再删除；中途失败就停止且不删除", async () => {
    const items = [info("A", "trend"), info("B", "momentum"), info("C", "trend")];
    const setCategory = vi.fn().mockResolvedValue(undefined);
    const deleteCategory = vi.fn().mockResolvedValue(undefined);
    await expect(deleteCategoryMovingFactors("trend", items, { setCategory, deleteCategory })).resolves.toEqual({
      deleted: true,
      moved: ["A", "C"]
    });
    expect(setCategory.mock.calls).toEqual([
      ["A", "uncategorized"],
      ["C", "uncategorized"]
    ]);
    expect(deleteCategory).toHaveBeenCalledWith("trend");

    setCategory.mockReset().mockResolvedValueOnce(undefined).mockRejectedValueOnce(new Error("boom"));
    deleteCategory.mockReset();
    const result = await deleteCategoryMovingFactors("trend", items, { setCategory, deleteCategory });
    expect(result).toMatchObject({ deleted: false, moved: ["A"], failed: "C" });
    expect(deleteCategory).not.toHaveBeenCalled();
  });
});

describe("定义表格与批量移动", () => {
  it("表格行把 factor_id、updated_at 提到顶层并带分类名、参数与操作状态", () => {
    const item: FactorInfo = {
      factor: def("Rsi", {
        category_id: "momentum",
        updated_at: "t1",
        params: [{ name: "window", type: "int", min: 2, max: 120, default_value: 14 }]
      }),
      usages: [{ task_id: "s1", status: "enabled" }]
    };
    const [row] = definitionRows([item], [{ category_id: "momentum", name: "动量类" }], id => `任务 ${id}`);
    expect(row).toMatchObject({ factor_id: "Rsi", updated_at: "t1", category: "动量类", paramNames: "window" });
    expect(row.chips[0].label).toBe("任务 s1");
    expect(row.edit.disabled).toBe(true);
    expect(row.remove.disabled).toBe(true);
  });

  it("逐个移动，中途失败就停止", async () => {
    const setCategory = vi.fn().mockResolvedValueOnce(undefined).mockRejectedValueOnce(new Error("boom"));
    const result = await moveFactorsToCategory(["A", "B", "C"], "trend", setCategory);
    expect(result).toMatchObject({ moved: ["A"], failed: "B" });
    expect(setCategory).toHaveBeenCalledTimes(2);
  });
});
