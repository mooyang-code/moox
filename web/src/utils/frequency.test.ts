import { describe, expect, it } from "vitest";
import { STANDARD_FREQUENCIES, frequencyMinutes, isCanonicalFrequency, normalizeFrequency, parseFrequency } from "./frequency";

describe("frequency", () => {
  it("normalizes aliases to canonical frequencies", () => {
    expect(normalizeFrequency("1H")).toBe("1h");
    expect(normalizeFrequency(" 60m ")).toBe("1h");
    expect(normalizeFrequency("1M")).toBe("1mo");
    expect(normalizeFrequency("1m")).toBe("1m");
    expect(normalizeFrequency("1D")).toBe("1d");
    expect(normalizeFrequency("240m")).toBe("4h");
    expect(normalizeFrequency("1440m")).toBe("1d");
    expect(normalizeFrequency("120m")).toBe("2h");
    expect(normalizeFrequency("12H")).toBe("12h");
  });

  it("accepts every divisor of one day as a canonical intraday period", () => {
    for (const value of ["45m", "2h", "3h", "12h", "288m", "90m"]) {
      expect(parseFrequency(value)).toBe(value);
      expect(isCanonicalFrequency(value)).toBe(true);
    }
  });

  it("leaves invalid values for validation to reject", () => {
    // 7m / 50m / 5h 不能整除一天；45M 的大写 M 是月；05m 带前导零；60M 别名已删除。
    for (const value of ["7m", "50m", "5h", "45M", "05m", "60M", "0m", "25h", "2d", "1.5h", ""]) {
      expect(parseFrequency(value)).toBeUndefined();
      expect(normalizeFrequency(value)).toBe(value.trim());
      expect(isCanonicalFrequency(value)).toBe(false);
    }
  });

  it("marks aliases as non canonical", () => {
    for (const value of ["1H", "1M", "60m", "1D", "120m", "1440m"]) {
      expect(isCanonicalFrequency(value)).toBe(false);
    }
    for (const value of STANDARD_FREQUENCIES) {
      expect(isCanonicalFrequency(value)).toBe(true);
    }
  });

  it("reports fixed durations in minutes", () => {
    expect(frequencyMinutes("45m")).toBe(45);
    expect(frequencyMinutes("2h")).toBe(120);
    expect(frequencyMinutes("1d")).toBe(1440);
    expect(frequencyMinutes("1w")).toBe(0);
    expect(frequencyMinutes("1mo")).toBe(0);
    expect(frequencyMinutes("7m")).toBe(0);
  });
});
