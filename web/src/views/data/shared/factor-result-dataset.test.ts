import { describe, expect, it } from "vitest";

import { factorResultDataset } from "./factor-result-dataset";

describe("factorResultDataset", () => {
  it("keeps the source dataset readable for normal IDs", () => {
    expect(factorResultDataset("dataset_binance_spot_kline_1m")).toBe("dataset_binance_spot_kline_1m_factor");
    expect(factorResultDataset("view_crypto_spot_kline_1m")).toBe("dataset_crypto_spot_kline_1m_factor");
  });

  it("keeps generated IDs within the storage dataset limit", () => {
    const id = factorResultDataset("a".repeat(50));
    expect(id.length).toBeLessThanOrEqual(50);
    expect(id).toMatch(/^[a-z][a-z0-9_]*$/);
  });
});
