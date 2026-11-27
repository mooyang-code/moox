import { describe, expect, it } from "vitest";

import { fieldValueTypeLabel, fillStrategyLabel, normalizeFieldValueType, statusLabel } from "./metadata-utils";

describe("statusLabel", () => {
  it("localizes enabled and disabled status values without changing API values", () => {
    expect(statusLabel("enabled")).toBe("已启用");
    expect(statusLabel("disabled")).toBe("已停用");
    expect(statusLabel("active")).toBe("已启用");
    expect(statusLabel("inactive")).toBe("已停用");
    expect(statusLabel("deleted")).toBe("已删除");
    expect(statusLabel("building")).toBe("building");
  });
});

describe("field value type & fill strategy helpers", () => {
  it("normalizes both Storage enum/number and Collector lowercase value types", () => {
    expect(normalizeFieldValueType("FIELD_VALUE_TYPE_DOUBLE")).toBe("double");
    expect(normalizeFieldValueType(3)).toBe("double");
    expect(normalizeFieldValueType("double")).toBe("double");
    expect(normalizeFieldValueType("INT")).toBe("int");
    expect(normalizeFieldValueType(5)).toBe("time");
    expect(normalizeFieldValueType("FIELD_VALUE_TYPE_UNSPECIFIED")).toBe("");
    expect(fieldValueTypeLabel("double")).toBe("浮点数");
    expect(fieldValueTypeLabel("FIELD_VALUE_TYPE_STRING")).toBe("字符串");
    expect(fieldValueTypeLabel("")).toBe("—");
  });

  it("formats fill strategies", () => {
    expect(fillStrategyLabel("prev")).toBe("前值填充");
    expect(fillStrategyLabel("ZERO")).toBe("补零");
    expect(fillStrategyLabel("")).toBe("—");
    expect(fillStrategyLabel("custom")).toBe("custom");
  });
});
