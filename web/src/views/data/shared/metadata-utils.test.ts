import { describe, expect, it } from "vitest";
import { dataKindOptions, displayFieldId, statusLabel, validateDatasetId, validateViewId } from "./metadata-utils";

describe("statusLabel", () => {
  it("localizes enabled and disabled status values without changing API values", () => {
    expect(statusLabel("enabled")).toBe("已启用");
    expect(statusLabel("disabled")).toBe("已停用");
    expect(statusLabel("active")).toBe("已启用");
    expect(statusLabel("inactive")).toBe("已停用");
    expect(statusLabel("building")).toBe("building");
  });
});

describe("dataKindOptions", () => {
  it("only exposes record and time-series datasets", () => {
    expect(dataKindOptions.map(item => item.value)).toEqual(["DATA_KIND_TIME_SERIES", "DATA_KIND_RECORD"]);
  });
});

describe("validateDatasetId", () => {
  it("requires the dataset_ or mdataset_ type prefix", () => {
    expect(validateDatasetId("dataset_stockcn_equity_kline")).toBe("");
    expect(validateDatasetId("mdataset_binance_kline_1m")).toBe("");
    expect(validateDatasetId("stockcn_equity_kline")).toContain("dataset_");
  });
});

describe("validateViewId", () => {
  it("requires the view_ type prefix", () => {
    expect(validateViewId("view_stockcn_equity_kline_1m")).toBe("");
    expect(validateViewId("stockcn_equity_kline_1m")).toContain("view_");
  });
});

describe("displayFieldId", () => {
  it("strips the internal dataset prefix from merged field ids", () => {
    expect(displayFieldId("dataset_binance_spot_kline_1m__close")).toBe("close");
    expect(displayFieldId("mdataset_binance_kline_1m__volume")).toBe("volume");
    expect(displayFieldId("dataset_a__b__close")).toBe("close");
  });

  it("keeps plain field ids untouched", () => {
    expect(displayFieldId("close")).toBe("close");
    expect(displayFieldId("volume_unit")).toBe("volume_unit");
    expect(displayFieldId("")).toBe("");
    expect(displayFieldId(undefined)).toBe("");
  });
});
