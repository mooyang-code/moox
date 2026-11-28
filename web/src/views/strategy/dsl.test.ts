import { describe, expect, it } from "vitest";
import {
  extractDiagnosticLine,
  mixedMarketTemplate,
  parseDSL,
  poolTemplate,
  rankedTemplate,
  summarizeDSL,
  timingTemplate,
  tunedTemplate
} from "./dsl";

describe("strategy DSL preview", () => {
  it("parses the v3 templates into read-only summaries without syntax errors", () => {
    const ranked = parseDSL(rankedTemplate);
    expect(ranked.syntaxError).toBe("");
    expect(ranked.preview).toMatchObject({ name: "binance_spot_momentum", bar: "1m", leverage: "1" });
    expect(ranked.preview?.rules).toEqual([
      { id: "long_momentum", name: "多头动量选币", type: "rank" },
      { id: "btc_trend", name: "BTC 上穿均线", type: "rank", timing: true }
    ]);
    expect(ranked.preview?.universe).toContain("排除 BTC-USDT");

    const aged = parseDSL(
      "name: a\nuniverse: {min_age_bars: 5}\nrules:\n  - {id: f, type: rank, allow_empty: true, universe: {max_age_bars: 30, age_markets: all}}\n"
    );
    expect(aged.preview?.universe).toBe("上市满 5 根；规则 f：上市不满 30 根，年龄按全部市场判断（允许为空）");

    const excluded = parseDSL("name: e\nrules:\n  - {id: g, type: rank, universe: {exclude_tags: [meme, new]}}\n");
    expect(excluded.preview?.universe).toBe("全部标的；规则 g：排除标签 meme、new");

    const timing = parseDSL(timingTemplate);
    expect(timing.syntaxError).toBe("");
    expect(timing.preview?.rules).toEqual([{ id: "majors_trend", name: "主流币均线择时", type: "rank", timing: true }]);
    expect(timingTemplate).toContain("bars[-1].bias");
  });

  it("extracts rule-level markets and ignores unknown market labels in preview", () => {
    const mixed = parseDSL(
      "name: m\nmarket: {select: both, prefer: spot}\nrules:\n  - {id: a, type: rank, market: swap}\n  - {id: b, type: rank}\n"
    );
    expect(mixed.syntaxError).toBe("");
    expect(mixed.preview?.rules.map(rule => rule.market)).toEqual(["swap", undefined]);
    expect(summarizeDSL("name: m\nrules:\n  - {id: a, type: rank, market: spot}\n")).toContain("a（rank，现货）");
    expect(parseDSL("name: m\nrules:\n  - {id: a, type: rank, market: both}\n").preview?.rules[0].market).toBeUndefined();
  });

  it("summarizes a definition in one line and reports YAML syntax failures with line numbers", () => {
    expect(summarizeDSL(rankedTemplate)).toContain("2 条规则");
    expect(summarizeDSL("name: [")).toBe("DSL 无法解析");
    expect(summarizeDSL("name: a")).toBe("DSL 没有规则");

    const broken = parseDSL("name: ok\nrules: [\n");
    expect(broken.preview).toBeNull();
    expect(broken.syntaxError).not.toBe("");
    expect(broken.syntaxErrorLine).toBeGreaterThan(0);

    const aliasBroken = parseDSL("name: test\nrules: *missing\n");
    expect(aliasBroken.preview).toBeNull();
    expect(aliasBroken.syntaxError).not.toBe("");
  });

  it("extracts 1-based YAML line numbers from ValidateStrategy diagnostics and ignores expression-local lines", () => {
    expect(extractDiagnosticLine(["策略 DSL 不是合法的 YAML（第 12 行附近）：缺少冒号"])).toBe(12);
    expect(extractDiagnosticLine(["yaml: line 9: mapping values are not allowed"])).toBe(9);
    expect(extractDiagnosticLine(["语法错误（第 1 行第 21 列）：表达式写法不合法"])).toBe(0);
    expect(extractDiagnosticLine(["rules[0] 缺少 id"])).toBe(0);
  });
});

describe("tuned and mixed-market templates", () => {
  it("parses tuned template and marks tuned flag", () => {
    const tuned = parseDSL(tunedTemplate);
    expect(tuned.syntaxError).toBe("");
    expect(tuned.preview?.tuned).toBe(true);
    expect(tuned.preview?.rules.map(rule => rule.id)).toEqual(["above_ma"]);
    expect(tunedTemplate).not.toContain("factors:");
    expect(tunedTemplate).toContain("factor_params:");
    expect(tunedTemplate).toContain("ma__var:");
    expect(tunedTemplate).toContain('params("ma__var", "window")');
  });

  it("describes market modes accurately", () => {
    expect(parseDSL(rankedTemplate).preview?.market).toBe("按计算任务的市场");
    expect(parseDSL("name: a\nmarket: {select: spot}\nrules:\n  - {id: r, type: rank}\n").preview?.market).toBe("选币范围 现货");
    expect(parseDSL(mixedMarketTemplate).preview?.market).toBe("选币范围 现货与永续，优先在永续下单");
    expect(mixedMarketTemplate).toContain("side: short");
    expect(mixedMarketTemplate).not.toContain("factor_params:");
  });
});

describe("策略池预览", () => {
  const head = "name: p\nbar: 1h\n";
  const members = "strategy_pool:\n  members:\n    - {id: a, strategy: s_a}\n    - {id: b, strategy: s_b}\n";
  const fixed = `${head}${members}  allocation: {method: fixed, ratios: {a: 0.6, b: 0.4}}\nportfolio: {leverage: 1}\n`;
  const drift = `${head}${members}  allocation:\n    method: drift\n    ratios: {a: 0.6, b: 0.4}\n`;

  it("解析策略池模板并生成只读摘要", () => {
    const result = parseDSL(poolTemplate);
    expect(result.syntaxError).toBe("");
    expect(result.preview?.rules).toEqual([]);
    expect(result.preview?.market).toBe("成员市场的并集（各成员按自己的 market）");
    expect(result.preview?.pool).toEqual({
      memberIds: ["a", "b"],
      method: "rotation",
      top: 1,
      timingWindow: 24,
      step: "0.2"
    });
    expect(summarizeDSL(poolTemplate)).toBe("1h · 策略池：2 个成员，轮动（前 1 名），择时 24 根，步长 0.2");
  });

  it("生成固定比例与漂移分配的摘要", () => {
    expect(summarizeDSL(fixed)).toBe("1h · 策略池：2 个成员，固定比例");
    const driftRich = `${drift}    start: "2026-01-17T07:00:00Z"\n    sides: {long: {rebalance: every_bar}}\n    switch:\n      - when: {empty: b}\n        move: {from: a, to: [b]}\n  exposure: {bands: [{min: 0, max: 0.3, adjust: auto, pivot: 1}]}\n  gross: {min: 1, max: 1.5}\n  freeze: {watch: long, window_bars: 1, threshold: 0.1, bars: 4}\n`;
    expect(parseDSL(driftRich).preview?.pool).toMatchObject({ method: "drift", driftStart: "2026-01-17T07:00:00Z" });
    expect(summarizeDSL(driftRich)).toBe(
      "1h · 策略池：2 个成员，漂移分配（起点 2026-01-17T07:00:00Z），条件转移，净敞口区间，总杠杆区间，波动冻结"
    );
    const initial = `${drift}    initial: {at: "2026-01-17", ratios: {a: 0.5, b: 0.5}}\n`;
    expect(summarizeDSL(initial)).toBe("1h · 策略池：2 个成员，漂移分配（起点 2026-01-17）");
    expect(summarizeDSL(drift)).toBe("1h · 策略池：2 个成员，漂移分配");
  });
});
