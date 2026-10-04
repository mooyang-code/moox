import { describe, expect, it } from "vitest";
import type { FactorDef, FactorSet, FactorSetInfo } from "@/api/factor/types";
import type { View, ViewColumn } from "@/api/storage/types";
import { buildInputBindings, canCombineSelections, findOutputColumn, normalizeFrequency, validFactorSets, validateAliasConflicts } from "./bindings";

const source = { space_id: "s", view_id: "price", name: "价格", dataset_id: "prices", status: "active" } as View;
const factor = { factor_id: "ma", set_id: "set-1", name: "MA", source_code: "", input_columns: ["close"], outputs: ["ma20"], params_json: "{}", lookback_periods: 20, status: "enabled" } as FactorDef;
const set: FactorSet = { set_id: "set-1", space_id: "s", source_dataset_id: "prices", freq: "1h", subject_mode: "all", subjects: [], result_dataset_id: "result", status: "enabled" };
const info: FactorSetInfo = { factor_set: set, factors: [factor] };

describe("strategy factor-set bindings", () => {
  it("maps factor output to the metadata column instead of guessing the name", () => {
    const column = { view_id: "factor_result", column_name: "ma_20_value", attributes: { origin_factor_id: "ma", factor_output: "ma20" } } as ViewColumn;
    expect(findOutputColumn([column], "ma", "ma20")?.column_name).toBe("ma_20_value");
  });

  it("only offers enabled sets matching source dataset and frequency", () => {
    const other = { ...info, factor_set: { ...set, set_id: "other", source_dataset_id: "elsewhere" } };
    const disabled = { ...info, factor_set: { ...set, set_id: "disabled", status: "disabled" as const } };
    expect(validFactorSets([info, other, disabled], source, "1H")).toEqual([info]);
  });

  it("rejects selections that do not share one result dataset or collide with the source", () => {
    const one = { factor, factorSet: set, output: "ma20", column_name: "ma_20_value", result_view_id: "view_result" };
    expect(canCombineSelections([one], source)).toEqual({ ok: true });
    expect(canCombineSelections([{ ...one, factorSet: { ...set, result_dataset_id: "other" } }, one], source).ok).toBe(false);
    expect(canCombineSelections([{ ...one, factorSet: { ...set, result_dataset_id: "prices" } }], source).ok).toBe(false);
  });

  it("serializes the factor-set contract without binding identifiers", () => {
    const selection = { factor, factorSet: set, output: "ma20", column_name: "ma_20_value", result_view_id: "view_result" };
    const value = JSON.parse(buildInputBindings(source, "1h", [selection]));
    expect(value.source_view_id).toBe("price");
    expect(value.factors[0]).toMatchObject({
      factor_id: "ma",
      set_id: "set-1",
      source_hash: "",
      input_columns: ["close"],
      params_json: "{}",
      lookback_periods: 20,
      frequency: "1H",
      result_dataset_id: "result",
      result_view_id: "view_result",
      output: "ma20",
      column_name: "ma_20_value",
      subject_mode: "all",
      subjects_json: "[]"
    });
    expect(Object.keys(value.factors[0]).sort()).toEqual([
      "column_name",
      "factor_id",
      "frequency",
      "input_columns",
      "lookback_periods",
      "output",
      "params_json",
      "result_dataset_id",
      "result_view_id",
      "set_id",
      "source_hash",
      "subject_mode",
      "subjects_json"
    ]);
    expect(value.factors[0]).not.toHaveProperty("binding_id");
  });

  it("compares backend frequency spellings and rejects output alias collisions", () => {
    expect(normalizeFrequency("1h")).toBe("1H");
    expect(normalizeFrequency("15m")).toBe("15m");
    expect(validateAliasConflicts([{ factor, factorSet: set, output: "close", column_name: "value", result_view_id: "view_result" }])).toContain("内置");
    expect(validateAliasConflicts([
      { factor, factorSet: set, output: "ma20", column_name: "value", result_view_id: "view_result" },
      { factor: { ...factor, factor_id: "rsi" }, factorSet: set, output: "rsi", column_name: "value", result_view_id: "view_result" }
    ])).toContain("多个");
  });
});
