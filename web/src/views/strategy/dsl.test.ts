import { describe, expect, it } from "vitest";
import { parseDSL, rankedTemplate, signalTemplate, summarizeDSL, tunedTemplate } from "./dsl";

describe("strategy DSL preview", () => {
  it("parses the v3 templates without diagnostics", () => {
    const ranked = parseDSL(rankedTemplate);
    expect(ranked.diagnostics).toEqual([]);
    expect(ranked.preview).toMatchObject({ name: "binance_spot_momentum", bar: "1m", leverage: "1" });
    expect(ranked.preview?.rules).toEqual([
      { id: "long_momentum", name: "多头动量选币", type: "rank", subStrategies: 0 },
      { id: "btc_trend", name: "BTC 上穿均线", type: "signal", subStrategies: 0 }
    ]);
    expect(ranked.preview?.universe).toContain("排除 BTC-USDT");
    const signal = parseDSL(signalTemplate);
    expect(signal.diagnostics).toEqual([]);
    expect(signalTemplate).toContain("bars[-1].bias");
  });

  it("reports structural problems before saving", () => {
    expect(parseDSL("name: a\nrules: {rank: {}}\n").diagnostics.map(item => item.message)).toContain(
      "rules 必须是非空列表，每条规则包含 id 与 type"
    );
    expect(parseDSL("name: a\nrules:\n  - id: r\n    type: ranked\n").diagnostics[0].message).toContain("rank、signal 或 tranche");
    expect(parseDSL("name: [").preview).toBeNull();
  });

  it("summarizes a definition in one line", () => {
    expect(summarizeDSL(rankedTemplate)).toContain("2 条规则");
    expect(summarizeDSL("name: [")).toBe("DSL 无法解析");
    expect(summarizeDSL("name: a")).toBe("DSL 没有规则");
  });

  it("reports YAML that fails to convert instead of throwing", () => {
    const result = parseDSL("name: test\nrules: *missing\n");
    expect(result.preview).toBeNull();
    expect(result.diagnostics.length).toBeGreaterThan(0);
  });
});

describe("tuned template", () => {
  it("declares the variable member parameters and passes the local precheck", () => {
    const tuned = parseDSL(tunedTemplate);
    expect(tuned.diagnostics).toEqual([]);
    expect(tuned.preview?.rules.map(rule => rule.id)).toEqual(["above_ma"]);
    // 不再有 factors 段（服务端会拒绝）：可变成员的参数由 factor_params 给出，规则直接写列名与 params()。
    expect(tunedTemplate).not.toContain("factors:");
    expect(tunedTemplate).toContain("factor_params:");
    expect(tunedTemplate).toContain("ma__var:");
    expect(tunedTemplate).toContain('params("ma__var", "window")');
  });
});

describe("tranche rules", () => {
  const f1 = `name: f1_demo
rules:
  - id: demo
    type: tranche
    preset: f1
    weight: {total: 1}
    tranche:
      hold_period: "{h}"
      long_factors: [{factor: "po__{s}"}]
    params:
      columns: [h, s]
      rows:
        - [1, 49]
        - [3, 65]
`;
  it("accepts tranche rules and counts sub-strategies", () => {
    const result = parseDSL(f1);
    expect(result.diagnostics).toEqual([]);
    expect(result.preview?.rules[0]).toMatchObject({ id: "demo", type: "tranche", subStrategies: 2 });
    expect(summarizeDSL(f1)).toContain("tranche，2 个子策略");
  });
  it("rejects mixing tranche with other rule types", () => {
    const mixed =
      f1 +
      `  - id: other
    type: rank
`;
    expect(parseDSL(mixed).diagnostics.map(item => item.message)).toContain("含 tranche 规则的策略只能全部是 tranche 规则");
  });
});
