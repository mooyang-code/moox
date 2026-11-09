import { describe, expect, it } from "vitest";
import {
  isPoolStrategy,
  mixedMarketTemplate,
  parseDSL,
  poolTemplate,
  rankedTemplate,
  signalTemplate,
  summarizeDSL,
  tunedTemplate
} from "./dsl";

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
    const aged = parseDSL(
      "name: a\nuniverse: {min_age_bars: 5}\nrules:\n  - {id: f, type: rank, allow_empty: true, universe: {max_age_bars: 30, age_markets: all}}\n"
    );
    expect(aged.preview?.universe).toBe("上市满 5 根；规则 f：上市不满 30 根，年龄按全部市场判断（允许为空）");
    const signal = parseDSL(signalTemplate);
    expect(signal.diagnostics).toEqual([]);
    expect(signalTemplate).toContain("bars[-1].bias");
  });

  it("reports structural problems before saving", () => {
    expect(parseDSL("name: a\nrules: {rank: {}}\n").diagnostics.map(item => item.message)).toContain(
      "rules 必须是非空列表，每条规则包含 id 与 type"
    );
    expect(parseDSL("name: a\nrules:\n  - id: r\n    type: ranked\n").diagnostics[0].message).toContain(
      "rank、signal 或 tranche"
    );
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
  const tranche = `name: tranche_demo
rules:
  - id: demo
    type: tranche
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
    const result = parseDSL(tranche);
    expect(result.diagnostics).toEqual([]);
    expect(result.preview?.rules[0]).toMatchObject({ id: "demo", type: "tranche", subStrategies: 2 });
    expect(summarizeDSL(tranche)).toContain("tranche，2 个子策略");
  });
  it("rejects mixing tranche with other rule types", () => {
    const mixed =
      tranche +
      `  - id: other
    type: rank
`;
    expect(parseDSL(mixed).diagnostics.map(item => item.message)).toContain("含 tranche 规则的策略只能全部是 tranche 规则");
  });
});

describe("market 预览", () => {
  it("没写 market 时按计算任务的市场，写了就说明选币范围与下单市场", () => {
    expect(parseDSL(rankedTemplate).preview?.market).toBe("按计算任务的市场");
    expect(parseDSL("name: a\nmarket: {select: spot}\nrules:\n  - {id: r, type: rank}\n").preview?.market).toBe("选币范围 现货");
    expect(parseDSL(mixedMarketTemplate).preview?.market).toBe("选币范围 现货与永续，优先在永续下单");
  });
});

describe("混合市场模板", () => {
  it("本地预检通过，声明双市场与只走永续的空头规则", () => {
    const result = parseDSL(mixedMarketTemplate);
    expect(result.diagnostics).toEqual([]);
    expect(result.preview?.rules.map(rule => rule.id)).toEqual(["long_momentum", "short_weak"]);
    expect(mixedMarketTemplate).toContain("market: {select: both, prefer: swap}");
    expect(mixedMarketTemplate).toContain("side: short");
    expect(mixedMarketTemplate).toContain("现货、永续各一个（两个任务的列名要一致），或统一数据集上的一个任务");
    expect(mixedMarketTemplate).not.toContain("type: tranche");
    expect(mixedMarketTemplate).not.toContain("factor_params:");
  });
});

describe("策略池", () => {
  const head = "name: p\nbar: 1h\n";
  const members = "strategy_pool:\n  members:\n    - {id: a, strategy: s_a}\n    - {id: b, strategy: s_b}\n";
  const fixed = `${head}${members}  allocation: {method: fixed, ratios: {a: 0.6, b: 0.4}}\nportfolio: {leverage: 1}\n`;
  const messages = (source: string) => parseDSL(source).diagnostics.map(item => item.message);

  it("策略池模板本地预检通过，预览给出成员数、分配方式、择时与步长", () => {
    const result = parseDSL(poolTemplate);
    expect(result.diagnostics).toEqual([]);
    expect(result.preview?.rules).toEqual([]);
    expect(result.preview?.market, "池不写 market，市场是成员的并集").toBe("成员市场的并集（各成员按自己的 market）");
    expect(result.preview?.pool).toEqual({
      memberIds: ["a", "b"],
      method: "rotation",
      top: 1,
      timingWindow: 24,
      step: "0.2"
    });
    expect(summarizeDSL(poolTemplate)).toBe("1h · 策略池：2 个成员，轮动（前 1 名），择时 24 根，步长 0.2");
    expect(poolTemplate).toContain("strategy: strategy_trend_a");
    expect(poolTemplate).toContain("预热完成前不能确认运行");
    expect(poolTemplate).not.toContain("factor_params:");
  });

  it("固定比例的策略池没有诊断，摘要写成员数与分配方式", () => {
    const result = parseDSL(fixed);
    expect(result.diagnostics).toEqual([]);
    expect(result.preview?.pool).toMatchObject({ memberIds: ["a", "b"], method: "fixed", timingWindow: 0, step: "" });
    expect(summarizeDSL(fixed)).toBe("1h · 策略池：2 个成员，固定比例");
    expect(
      summarizeDSL(
        poolTemplate
          .replace("top: 1", "top: 2")
          .replace(/  timing[^\n]*\n/, "")
          .replace(/  step[^\n]*\n/, "")
      )
    ).toBe("1h · 策略池：2 个成员，轮动（前 2 名）");
  });

  it("同时写 rules 与 strategy_pool 报错", () => {
    const both = `${fixed}rules:\n  - {id: r, type: rank}\n`;
    expect(messages(both)).toContain("rules 与 strategy_pool 只能写一个：策略池的逻辑都在成员策略里");
  });

  it("缺 members、缺 allocation.method、成员写得不全都报错", () => {
    expect(messages(`${head}strategy_pool: {allocation: {method: fixed}}\n`)).toContain(
      "strategy_pool.members 必须是非空列表，每个成员写 id 与 strategy"
    );
    expect(messages(`${head}${members}`)).toContain("strategy_pool.allocation.method 必须写 fixed 或 rotation");
    expect(messages(`${head}${members}  allocation: {method: other}\n`)).toContain(
      "strategy_pool.allocation.method 只能是 fixed 或 rotation"
    );
    const broken = `${head}strategy_pool:\n  members:\n    - {id: a}\n    - {strategy: s}\n    - {id: a, strategy: s}\n  allocation: {method: fixed, ratios: {a: 1}}\n`;
    const list = messages(broken);
    expect(list).toContain("strategy_pool.members[0] 缺少 strategy");
    expect(list).toContain("strategy_pool.members[1] 缺少 id");
    expect(list).toContain("strategy_pool.members 的成员 id a 重复");
    expect(messages(`${head}strategy_pool: [a]\n`)).toContain("strategy_pool 必须是对象，包含 members 与 allocation");
  });

  it("fixed 的 ratios 必须覆盖所有成员，rotation 要写因子与名次", () => {
    expect(messages(`${head}${members}  allocation: {method: fixed, ratios: {a: 1}}\n`)).toContain(
      "allocation.ratios 缺少成员 b 的比例（不分配请写 0）"
    );
    expect(messages(`${head}${members}  allocation: {method: fixed}\n`)).toContain(
      "allocation.method 是 fixed，需要写 ratios（成员 id → 比例）"
    );
    expect(messages(`${head}${members}  allocation: {method: rotation}\n`)).toEqual(
      expect.arrayContaining(["allocation.method 是 rotation，需要写 rotation.factors", "rotation.top 必须是不小于 1 的整数"])
    );
    const badFactor = `${head}${members}  allocation: {method: rotation, rotation: {factors: [{name: sharpe, window: 24}], top: 1}}\n`;
    expect(messages(badFactor)).toContain("rotation.factors[0] 的 name 只能是 return、ma_ratio、drawdown");
  });

  it("池本身不写 market、universe、factor_params，必须写 bar", () => {
    const list = messages(
      `name: p\nmarket: {select: spot}\nuniverse: {min_age_bars: 10}\nfactor_params:\n  x:\n    window: "1"\n${members}  allocation: {method: fixed, ratios: {a: 1, b: 0}}\n`
    );
    expect(list).toEqual(
      expect.arrayContaining([
        "策略池不写 market：成员各自显式写 market，池要绑定的计算任务是成员所需市场的并集",
        "策略池必须写 bar：成员的周期要与池一致",
        "策略池不写 universe：标的范围由各成员自己的 universe 决定",
        "策略池不支持 factor_params：多个成员可能给同一个可变成员写参数，会冲突"
      ])
    );
  });

  it("什么都没写时提示可以写 strategy_pool", () => {
    expect(messages("name: a\n")).toContain("rules 必须是非空列表，每条规则包含 id 与 type（策略池请写 strategy_pool）");
  });
});

describe("策略池判断（回放入口据此禁用）", () => {
  it("策略池为真；普通策略、空文本、解析失败的为假", () => {
    expect(isPoolStrategy(poolTemplate)).toBe(true);
    expect(isPoolStrategy(rankedTemplate)).toBe(false);
    expect(isPoolStrategy("")).toBe(false);
    expect(isPoolStrategy(undefined)).toBe(false);
    expect(isPoolStrategy("name: [")).toBe(false);
  });
});
