import { describe, expect, it } from "vitest";
import { isCanonicalFrequency, normalizeFrequency } from "./frequency";

describe("frequency", () => {
  it("normalizes aliases to canonical frequencies", () => {
    expect(normalizeFrequency("1H")).toBe("1h");
    expect(normalizeFrequency(" 60m ")).toBe("1h");
    expect(normalizeFrequency("1M")).toBe("1mo");
    expect(normalizeFrequency("1m")).toBe("1m");
    expect(normalizeFrequency("1D")).toBe("1d");
  });

  it("leaves unknown values for validation to reject", () => {
    expect(normalizeFrequency("7m")).toBe("7m");
    expect(isCanonicalFrequency("7m")).toBe(false);
    expect(isCanonicalFrequency("1H")).toBe(false);
    expect(isCanonicalFrequency("1mo")).toBe(true);
  });
});
