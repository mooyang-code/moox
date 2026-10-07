import { describe, expect, it } from "vitest";

import { displayFieldId, statusLabel } from "./metadata-utils";

const unsupportedDatasetId = "m" + "dataset_binance_kline_1m";

describe("statusLabel", () => {
  it("localizes enabled and disabled status values without changing API values", () => {
    expect(statusLabel("enabled")).toBe("已启用");
    expect(statusLabel("disabled")).toBe("已停用");
    expect(statusLabel("active")).toBe("已启用");
    expect(statusLabel("inactive")).toBe("已停用");
    expect(statusLabel("building")).toBe("building");
  });
});

describe("displayFieldId", () => {
  it("strips a dataset prefix from internal field ids", () => {
    expect(displayFieldId("dataset_binance_kline_1m__close")).toBe("close");
    expect(displayFieldId(`${unsupportedDatasetId}__volume`)).toBe(`${unsupportedDatasetId}__volume`);
    expect(displayFieldId("dataset_a__b__close")).toBe("close");
  });

  it("keeps plain field ids untouched", () => {
    expect(displayFieldId("close")).toBe("close");
    expect(displayFieldId("volume_unit")).toBe("volume_unit");
    expect(displayFieldId("")).toBe("");
    expect(displayFieldId(undefined)).toBe("");
  });
});
