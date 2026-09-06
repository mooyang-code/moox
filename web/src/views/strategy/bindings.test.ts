import { describe, expect, it } from "vitest";
import type { FactorBinding, FactorDef } from "@/api/factor/types";
import type { View, ViewColumn } from "@/api/storage/types";
import { buildInputBindings, canCombineSelections, findOutputColumn, normalizeFrequency, validBindings, validateAliasConflicts } from "./bindings";

const source = { space_id: "s", view_id: "price", name: "价格", primary_dataset_id: "d", status: "active" } as View;
const factor = { factor_id: "ma", name: "MA", source_code: "", input_columns: ["close"], outputs: ["ma20"], params_json: "{}", lookback_periods: 20, status: "enabled" } as FactorDef;
const binding = { binding_id: "b1", factor_id: "ma", space_id: "s", source_view_id: "price", result_dataset_id: "rd", result_view_id: "factor_result", freq: "1h", subject_mode: "all", subjects_json: "{}", status: "enabled" } as FactorBinding;

describe("strategy bindings", () => {
  it("maps factor output to the metadata column instead of guessing the name", () => {
    const column = { view_id: "factor_result", column_name: "ma_20_value", attributes: { origin_factor_id: "ma", factor_output: "ma20" } } as ViewColumn;
    expect(findOutputColumn([column], "ma", "ma20")?.column_name).toBe("ma_20_value");
  });
  it("rejects multiple result views and source/result collisions", () => {
    const one = { factor, binding, output: "ma20", column_name: "ma_20_value" };
    expect(canCombineSelections([one], source)).toEqual({ ok: true });
    expect(canCombineSelections([{ ...one, binding: { ...binding, result_view_id: "other" } }, { ...one, binding: { ...binding, binding_id: "b2", result_view_id: "third" } }], source).ok).toBe(false);
    expect(canCombineSelections([{ ...one, binding: { ...binding, result_view_id: "price" } }], source).ok).toBe(false);
  });
  it("serializes the exact instance binding shape", () => {
    const value = JSON.parse(buildInputBindings(source, "1h", [{ factor, binding, output: "ma20", column_name: "ma_20_value" }]));
    expect(value.source_view_id).toBe("price");
    expect(value.factors[0]).toMatchObject({ factor_id: "ma", binding_id: "b1", result_view_id: "factor_result", output: "ma20", column_name: "ma_20_value" });
  });
  it("compares backend frequency spellings without changing minute semantics", () => {
    expect(normalizeFrequency("1h")).toBe("1H");
    expect(normalizeFrequency("15m")).toBe("15m");
    expect(validBindings([{ ...binding, freq: "1H" }], "price", "1h")).toHaveLength(1);
  });
  it("rejects aliases that collide with built-ins or another factor", () => {
    expect(validateAliasConflicts([{ factor, binding, output: "close", column_name: "value" }])).toContain("内置");
    expect(validateAliasConflicts([{ factor, binding, output: "ma20", column_name: "value" }, { factor: { ...factor, factor_id: "rsi" }, binding: { ...binding, binding_id: "b2" }, output: "rsi", column_name: "value" }])).toContain("多个");
  });
});
