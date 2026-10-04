import { describe, expect, it } from "vitest";
import { parseDSL, rankedTemplate, requiredBarFields, requiredFactorFields, signalTemplate } from "./dsl";

describe("strategy DSL parser", () => {
  it("returns a compact preview from structured YAML", () => {
    expect(parseDSL(rankedTemplate).preview).toMatchObject({ name: "收盘价排序示例", bar: "1h", calendar: "crypto_24x7", rules: ["rank"] });
  });
  it("reports duplicate keys and malformed YAML without erasing source", () => {
    const result = parseDSL("name: one\nname: two\n");
    expect(result.preview).toBeNull();
    expect(result.diagnostics.length).toBeGreaterThan(0);
  });
  it("keeps the previous-bar expression visible in the signal template", () => {
    expect(signalTemplate).toContain("bars[-1].ma20 <= bars[-1].close");
    expect(parseDSL(signalTemplate).diagnostics).toEqual([]);
  });
  it("identifies non-OHLCV fields that need factor bindings", () => {
    expect(requiredBarFields("bars[-1].ma20 > bars[0].close && bars[0].open > 0")).toEqual(["ma20"]);
    expect(requiredFactorFields(`rules:
  r:
    score: momentum
    filter_before: close > momentum
    select:
      where: 'instrument_id in ["BTC-USDT-SPOT"] && return_20 > 0'`)).toEqual(["momentum", "return_20"]);
  });
});
