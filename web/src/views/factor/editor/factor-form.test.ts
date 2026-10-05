import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";
import type { DatasetColumn } from "@/api/storage/types";
import {
  blankFactorForm,
  buildFactorPayload,
  checklistBlockers,
  factorChecklist,
  formFromDefinition,
  sourceInputColumns,
  validateFactorParamsJSON,
  type FactorFormState
} from "./factor-form";

const column = (name: string, patch: Partial<DatasetColumn> = {}): DatasetColumn => ({
  space_id: "crypto",
  dataset_id: "d",
  column_name: name,
  origin_type: "DATASET_COLUMN_ORIGIN_TYPE_FIELD",
  origin_id: name,
  value_type: "FIELD_VALUE_TYPE_DOUBLE",
  status: "COLUMN_STATUS_ACTIVE",
  ...patch
});

const valid = (patch: Partial<FactorFormState> = {}): FactorFormState => ({
  ...blankFactorForm(),
  factor_id: "Bias",
  name: "Bias",
  input_columns: ["close"],
  outputs: ["bias_20"],
  source_code: "def compute(df, params, context):\n    return df",
  ...patch
});

const failing = (form: FactorFormState) => factorChecklist(form).filter(item => !item.ok);

describe("validateFactorParamsJSON", () => {
  it("keeps large numbers verbatim and requires an object", () => {
    const raw = ` { "large": 9007199254740993, "huge": 1e400 } `;
    expect(validateFactorParamsJSON(raw)).toBe(raw.trim());
    expect(validateFactorParamsJSON("  ")).toBe("{}");
    expect(() => validateFactorParamsJSON("[]")).toThrow("JSON object");
  });
});

describe("sourceInputColumns", () => {
  it("keeps active business columns and drops system, inactive and reserved ones", () => {
    const columns = [
      column("close"),
      column("open"),
      column("data_time"),
      column("series_tag"),
      column("internal", { origin_type: "DATASET_COLUMN_ORIGIN_TYPE_SYSTEM" }),
      column("numeric_system", { origin_type: 3 }),
      column("retired", { status: "COLUMN_STATUS_DISABLED" })
    ];
    expect(sourceInputColumns(columns)).toEqual(["close", "open"]);
  });
});

describe("factorChecklist (static checks of the backend ValidateDefinition)", () => {
  it("passes a complete form", () => {
    expect(failing(valid())).toEqual([]);
    expect(checklistBlockers(factorChecklist(valid()))).toEqual([]);
  });

  it("id valid and not empty", () => {
    expect(failing(valid({ factor_id: "" })).map(item => item.key)).toContain("factor_id");
    expect(failing(valid({ factor_id: "1bad" })).map(item => item.key)).toContain("factor_id");
    expect(failing(valid({ factor_id: "ok_1" })).map(item => item.key)).not.toContain("factor_id");
  });

  it("inputs non-empty without duplicates", () => {
    const keys = (input_columns: string[]) => failing(valid({ input_columns })).map(item => item.key);
    expect(keys([])).toContain("input_columns");
    expect(keys(["close", "close"])).toContain("input_columns");
    expect(keys(["bad name"])).toContain("input_columns");
    expect(keys(["close", "open"])).not.toContain("input_columns");
  });

  it("does not require input columns to exist in any dataset", () => {
    expect(failing(valid({ input_columns: ["funding_rate"] }))).toEqual([]);
  });

  it("outputs non-empty, no reserved columns, no duplicates", () => {
    const keys = (outputs: string[]) => failing(valid({ outputs })).map(item => item.key);
    expect(keys([])).toContain("outputs");
    expect(keys(["data_time"])).toContain("outputs");
    expect(keys(["freq"])).toContain("outputs");
    expect(keys(["a", "a"])).toContain("outputs");
    expect(keys(["1x"])).toContain("outputs");
    expect(keys(["a", "b"])).not.toContain("outputs");
  });

  it("params must be a JSON object", () => {
    const keys = (params_json: string) => failing(valid({ params_json })).map(item => item.key);
    expect(keys("[]")).toContain("params_json");
    expect(keys("{")).toContain("params_json");
    expect(keys(`{"window":20}`)).not.toContain("params_json");
  });

  it("lookback at least 1", () => {
    const keys = (lookback_periods: number) => failing(valid({ lookback_periods })).map(item => item.key);
    expect(keys(0)).toContain("lookback_periods");
    expect(keys(1.5)).toContain("lookback_periods");
    expect(keys(1)).not.toContain("lookback_periods");
  });

  it("allow_partial_universe only for cross_section", () => {
    const keys = (patch: Partial<FactorFormState>) => failing(valid(patch)).map(item => item.key);
    expect(keys({ factor_type: "timeseries", allow_partial_universe: true })).toContain("allow_partial_universe");
    expect(keys({ factor_type: "cross_section", allow_partial_universe: true })).not.toContain("allow_partial_universe");
  });

  it("requires a module name and source code", () => {
    expect(failing(valid({ name: " " })).map(item => item.key)).toContain("name");
    expect(failing(valid({ source_code: "  \n" })).map(item => item.key)).toContain("source_code");
  });

  it("every failed item explains itself", () => {
    for (const item of failing(valid({ factor_id: "", input_columns: [], outputs: [] }))) {
      expect(item.message.length).toBeGreaterThan(0);
    }
  });
});

describe("buildFactorPayload", () => {
  it("submit payload has no set_id or status", () => {
    const payload = buildFactorPayload(valid({ params_json: `  {"window":20}  ` }));
    expect(payload).not.toHaveProperty("set_id");
    expect(payload).not.toHaveProperty("status");
    expect(payload).toMatchObject({ factor_id: "Bias", params_json: `{"window":20}`, allow_partial_universe: false });
  });

  it("drops allow_partial_universe for time-series factors", () => {
    expect(buildFactorPayload(valid({ factor_type: "timeseries", allow_partial_universe: true })).allow_partial_universe).toBe(
      false
    );
    expect(buildFactorPayload(valid({ factor_type: "cross_section", allow_partial_universe: true })).allow_partial_universe).toBe(
      true
    );
  });

  it("round-trips a stored definition", () => {
    const form = formFromDefinition({
      factor_id: "Bias",
      name: "Bias",
      factor_type: "cross_section",
      source_code: "x",
      source_hash: "h",
      input_columns: ["close"],
      outputs: ["bias"],
      params_json: "{}",
      lookback_periods: 5,
      allow_partial_universe: true,
      created_at: "",
      updated_at: ""
    });
    expect(buildFactorPayload(form)).toEqual({
      factor_id: "Bias",
      name: "Bias",
      factor_type: "cross_section",
      source_code: "x",
      input_columns: ["close"],
      outputs: ["bias"],
      params_json: "{}",
      lookback_periods: 5,
      allow_partial_universe: true
    });
  });
});

describe("editor page contract", () => {
  const page = fs.readFileSync(path.resolve(__dirname, "index.vue"), "utf8");

  it("editor has no 因子集 / 源数据集 / 频率 selector", () => {
    expect(page).not.toContain("因子集");
    expect(page).not.toContain('label="源数据集"');
    expect(page).not.toContain('label="频率"');
    expect(page).toContain("参考数据集");
  });

  it("editor never calls createFactorSet", () => {
    expect(page).not.toContain("createFactorSet");
    expect(page).toContain("createFactor");
    expect(page).toContain("updateFactor");
  });

  it("editor registers onBeforeRouteLeave", () => {
    expect(page).toContain("onBeforeRouteLeave");
  });

  it("editor uses code-editor and no a-textarea for source", () => {
    expect(page).toContain("AsyncCodeEditor");
    expect(page).toContain('v-model="form.source_code"');
    expect(page).not.toMatch(/<a-textarea[^>]*source_code/);
  });

  it("reference dataset is optional and not part of the payload", () => {
    expect(page).toContain("referenceDatasetId");
    expect(fs.readFileSync(path.resolve(__dirname, "factor-form.ts"), "utf8")).not.toContain("referenceDatasetId");
    expect(buildFactorPayload(valid())).not.toHaveProperty("referenceDatasetId");
  });
});
