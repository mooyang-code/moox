import { describe, expect, it } from "vitest";
import { parseDSL, rankedTemplate, signalTemplate, summarizeDSL } from "./dsl";

describe("strategy DSL preview", () => {
  it("parses the v3 templates without diagnostics", () => {
    const ranked = parseDSL(rankedTemplate);
    expect(ranked.diagnostics).toEqual([]);
    expect(ranked.preview).toMatchObject({ name: "binance_spot_momentum", bar: "1m", leverage: "1" });
    expect(ranked.preview?.rules).toEqual([
      { id: "long_momentum", name: "多头动量选币", type: "rank" },
      { id: "btc_trend", name: "BTC 上穿均线", type: "signal" }
    ]);
    expect(ranked.preview?.universe).toContain("排除 BTC-USDT");
    const signal = parseDSL(signalTemplate);
    expect(signal.diagnostics).toEqual([]);
    expect(signalTemplate).toContain("bars[-1].bias_20");
  });

  it("reports structural problems before saving", () => {
    expect(parseDSL("name: a\nrules: {rank: {}}\n").diagnostics.map(item => item.message)).toContain(
      "rules 必须是非空列表，每条规则包含 id 与 type"
    );
    expect(parseDSL("name: a\nrules:\n  - id: r\n    type: ranked\n").diagnostics[0].message).toContain("rank 或 signal");
    expect(parseDSL("name: [").preview).toBeNull();
  });

  it("summarizes a definition in one line", () => {
    expect(summarizeDSL(rankedTemplate)).toContain("2 条规则");
    expect(summarizeDSL("name: [")).toBe("DSL 无法解析");
    expect(summarizeDSL("name: a")).toBe("DSL 没有规则");
  });
});
