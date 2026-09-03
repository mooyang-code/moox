import { describe, expect, it } from "vitest";
import { dataKindOptions, validateDatasetId, validateViewId } from "./metadata-utils";

describe("dataKindOptions", () => {
  it("only exposes record and time-series datasets", () => {
    expect(dataKindOptions.map(item => item.value)).toEqual(["DATA_KIND_TIME_SERIES", "DATA_KIND_RECORD"]);
  });
});

describe("validateDatasetId", () => {
  it("requires the dataset_ type prefix", () => {
    expect(validateDatasetId("dataset_stockcn_equity_kline")).toBe("");
    expect(validateDatasetId("stockcn_equity_kline")).toContain("dataset_");
  });
});

describe("validateViewId", () => {
  it("requires the view_ type prefix", () => {
    expect(validateViewId("view_stockcn_equity_kline_1m")).toBe("");
    expect(validateViewId("stockcn_equity_kline_1m")).toContain("view_");
  });
});
