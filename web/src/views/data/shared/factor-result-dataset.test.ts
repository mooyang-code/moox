import { describe, expect, it } from "vitest";

import { factorResultDataset } from "./factor-result-dataset";

describe("factorResultDataset", () => {
  it("keeps the source dataset readable for normal IDs", () => {
    expect(factorResultDataset("binance_spot_kline_1m")).toBe("binance_spot_kline_1m_factor");
    expect(factorResultDataset("binance_spot_kline_1m_view")).toBe("binance_spot_kline_1m_factor");
  });

  it("keeps generated IDs within the storage dataset limit", () => {
    const id = factorResultDataset("a".repeat(50));
    expect(id.length).toBeLessThanOrEqual(50);
    expect(id).toMatch(/^[a-z][a-z0-9_]*$/);
  });
});
