import { describe, expect, it } from "vitest";
import type { DatasetColumn } from "@/api/storage/types";
import { checkInputs, checkOutputs, sourceInputColumns, validateFactorParamsJSON } from "./factor-form";

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

describe("checkInputs", () => {
  it("requires at least one input that exists in the source dataset", () => {
    expect(checkInputs([], ["close"])).toContain("不能为空");
    expect(checkInputs(["volume"], ["close"])).toContain("volume");
    expect(checkInputs(["close"], ["close"])).toBe("");
  });
});

describe("checkOutputs", () => {
  const context = { sourceColumns: ["close", "open"], siblingOutputs: ["rsi_14"] };

  it("accepts fresh identifier outputs", () => {
    expect(checkOutputs(["bias_20", "bias_60"], context)).toBe("");
  });

  it.each([
    [[], "不能为空"],
    [["1bad"], "合法列名"],
    [["data_time"], "保留列"],
    [["a", "a"], "重复"],
    [["close"], "源数据集"],
    [["rsi_14"], "其他因子"]
  ])("rejects %j", (outputs, message) => {
    expect(checkOutputs(outputs as string[], context)).toContain(message);
  });
});
