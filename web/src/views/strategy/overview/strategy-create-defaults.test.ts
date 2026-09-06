import { describe, expect, it } from "vitest";
import { parseDSL, rankedTemplate, signalTemplate } from "@/views/strategy/dsl";

describe("strategy DSL templates", () => {
  it("uses the current declarative DSL without legacy fields", () => {
    const result = parseDSL(rankedTemplate);
    expect(result.diagnostics).toEqual([]);
    expect(result.preview).toMatchObject({ name: "收盘价排序示例", bar: "1h", calendar: "crypto_24x7" });
    expect(rankedTemplate).not.toContain("api_version:");
    expect(rankedTemplate).not.toContain("kind:");
  });

  it("provides a signal template using bars[-1] for the previous bar", () => {
    expect(signalTemplate).toContain("bars[-1].ma20");
    expect(parseDSL(signalTemplate).diagnostics).toEqual([]);
  });
});
