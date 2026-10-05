import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";
import type { FactorDef, FactorInfo, FactorUsage } from "@/api/factor/types";
import { definitionDeleteState, definitionEditState, filterDefinitions, usageChips, usageCounts } from "./definitions-model";

const def = (id: string, patch: Partial<FactorDef> = {}): FactorDef => ({
  factor_id: id,
  name: `${id}_module`,
  factor_type: "timeseries",
  source_hash: "h",
  input_columns: ["close"],
  outputs: [id.toLowerCase()],
  params_json: "{}",
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
  info("Bias", [{ set_id: "set_a", status: "enabled" }]),
  info("Momentum", [{ set_id: "set_b", status: "disabled" }], { factor_type: "cross_section" }),
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
    const labels: Record<string, string> = { set_a: "现货K线 · 1m" };
    const chips = usageChips(
      [
        { set_id: "set_a", status: "enabled" },
        { set_id: "set_gone", status: "disabled" }
      ],
      setId => labels[setId] ?? setId
    );
    expect(chips).toEqual([
      { setId: "set_a", label: "现货K线 · 1m", status: "enabled", statusLabel: "已启用" },
      { setId: "set_gone", label: "set_gone", status: "disabled", statusLabel: "已停用" }
    ]);
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

  it("definitions page has no enable/disable action and no status column", () => {
    expect(page).not.toContain("setFactorMemberStatus");
    expect(page).not.toContain('title="状态"');
    expect(page).not.toContain('title="最近周期"');
    expect(page).toContain('title="使用情况"');
    expect(page).toContain("未被使用");
  });

  it("reads definitions without source and fetches the source only for the detail view", () => {
    expect(page).toContain("listFactors");
    expect(page).not.toContain("include_source");
    expect(fs.readFileSync(path.resolve(__dirname, "factor-detail-drawer.vue"), "utf8")).toContain("getFactor");
  });
});
