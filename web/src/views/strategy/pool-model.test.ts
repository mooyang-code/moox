import { describe, expect, it } from "vitest";
import {
  POOL_PALETTE,
  bankruptMemberIds,
  buildPoolPeriods,
  equityRange,
  equitySeries,
  lineSegments,
  memberColor,
  parsePoolPeriod,
  parsePoolSummary,
  poolMemberIds,
  poolPeriodNotes,
  poolNumber,
  ratioAxisMax,
  ratioSeries,
  ratioTotals,
  sessionBreaks,
  timingSeries
} from "./pool-model";

function member(id: string, extra: Record<string, unknown> = {}) {
  return { id, status: "ok", equity: 1, base: "0.5", ratio_before: "0.5", ratio: "0.5", gross: "0.4", net: "0.4", ...extra };
}

function result(
  id: string,
  time: string,
  members: unknown[],
  extra: Record<string, unknown> = {},
  pool: Record<string, unknown> = {}
) {
  return {
    result_id: id,
    session_id: "s1",
    bar_end_time: time,
    status: "ok",
    summary_json: JSON.stringify({ gross: "0.8", pool: { method: "fixed", members, ...pool } }),
    ...extra
  };
}

describe("策略池期解析", () => {
  it("没有 pool、JSON 损坏、时间无效或没有成员的期被跳过", () => {
    expect(parsePoolPeriod({ ...result("r", "2026-11-01T00:00:00Z", []), summary_json: "{}" })).toBeNull();
    expect(parsePoolPeriod({ ...result("r", "2026-11-01T00:00:00Z", []), summary_json: "" })).toBeNull();
    expect(parsePoolPeriod({ ...result("r", "2026-11-01T00:00:00Z", []), summary_json: "{坏的" })).toBeNull();
    expect(parsePoolPeriod(result("r", "不是时间", [member("a")]))).toBeNull();
    expect(parsePoolPeriod(result("r", "2026-11-01T00:00:00Z", []))).toBeNull();
    expect(parsePoolPeriod({ ...result("r", "2026-11-01T00:00:00Z", []), summary_json: '{"pool":{"members":"a"}}' })).toBeNull();
    expect(buildPoolPeriods([{ ...result("r", "2026-11-01T00:00:00Z", []), summary_json: '{"gross":"0.5"}' }])).toEqual([]);
  });

  it("把成员明细里的定点小数字符串转成数字，空值保持为 null", () => {
    const period = parsePoolPeriod(
      result(
        "r",
        "2026-11-01T00:00:00Z",
        [
          member("a", { timing: "1", rank: 1.5, targets: { "BTC-USDT|spot": "0.2", "ETH-USDT|swap": "-0.3" } }),
          { id: "b", status: "ok" }
        ],
        {},
        { timing: true }
      )
    )!;
    expect(period.timing).toBe(true);
    expect(period.method).toBe("fixed");
    const [a, b] = period.members;
    expect(a).toMatchObject({
      id: "a",
      equity: 1,
      base: 0.5,
      ratioBefore: 0.5,
      ratio: 0.5,
      gross: 0.4,
      net: 0.4,
      rank: 1.5,
      timing: 1
    });
    // 按贡献权重绝对值从大到小，标的与市场拆开
    expect(a.targets).toEqual([
      { key: "ETH-USDT|swap", instrumentId: "ETH-USDT", market: "swap", weight: -0.3 },
      { key: "BTC-USDT|spot", instrumentId: "BTC-USDT", market: "spot", weight: 0.2 }
    ]);
    expect(b).toMatchObject({ equity: null, base: null, timing: null, ratio: 0, rank: null, gross: null, targets: [] });
  });

  it("skipped 成员保留原因与详情，池整体跳过的期也有成员明细", () => {
    const period = parsePoolPeriod(
      result(
        "r",
        "2026-11-01T00:00:00Z",
        [member("a", { status: "skipped", reason: "factor_missing", detail: "缺少 bias" }), member("b")],
        { status: "skipped" }
      )
    )!;
    expect(period.resultStatus).toBe("skipped");
    expect(period.members[0]).toMatchObject({ status: "skipped", reason: "factor_missing", detail: "缺少 bias" });
    expect(period.members[1].status).toBe("ok");
  });

  it("同一期里重复的成员 id 只取第一个，没有 id 的成员忽略", () => {
    const period = parsePoolPeriod(
      result("r", "2026-11-01T00:00:00Z", [member("a", { equity: 1.1 }), member("a", { equity: 9 }), { status: "ok" }, null])
    )!;
    expect(period.members.map(item => [item.id, item.equity])).toEqual([["a", 1.1]]);
  });
});

describe("策略池期列表与序列", () => {
  const results = [
    // 接口按期倒序返回；中间缺一期、还混着没有 pool 的期
    result("r3", "2026-11-01T03:00:00Z", [
      member("a", { equity: 1.03 }),
      member("b", { equity: 0.9, bankrupt: true, ratio: "0" })
    ]),
    { ...result("rx", "2026-11-01T02:00:00Z", []), summary_json: '{"gross":"0.1"}' },
    result(
      "r1",
      "2026-11-01T01:00:00Z",
      [member("a", { equity: 1.01, timing: "1" }), member("b", { equity: 0.98, timing: "0" })],
      {},
      { timing: true }
    )
  ];

  it("乱序输入按时间正序排列，缺期不补，同一 result_id 只算一次", () => {
    const periods = buildPoolPeriods([...results, results[0]]);
    expect(periods.map(item => item.resultId)).toEqual(["r1", "r3"]);
    expect(poolMemberIds(periods)).toEqual(["a", "b"]);
  });

  it("构造净值、比例、择时序列并识别爆仓成员", () => {
    const periods = buildPoolPeriods(results);
    expect(equitySeries(periods)).toEqual([
      { id: "a", values: [1.01, 1.03] },
      { id: "b", values: [0.98, 0.9] }
    ]);
    expect(ratioSeries(periods).map(item => item.values)).toEqual([
      [0.5, 0.5],
      [0.5, 0]
    ]);
    expect(timingSeries(periods).map(item => item.values)).toEqual([
      [1, null],
      [0, null]
    ]);
    expect([...bankruptMemberIds(periods)]).toEqual(["b"]);
    expect(ratioTotals(periods)).toEqual([1, 0.5]);
  });

  it("个别期缺成员时，序列在那一期为 null，比例按 0 计，成员顺序按首次出现", () => {
    const periods = buildPoolPeriods([
      result("r2", "2026-11-01T02:00:00Z", [member("a"), member("c", { equity: 1.2 })]),
      result("r1", "2026-11-01T01:00:00Z", [member("a")])
    ]);
    expect(poolMemberIds(periods)).toEqual(["a", "c"]);
    expect(equitySeries(periods)[1].values).toEqual([null, 1.2]);
    expect(ratioSeries(periods)[1].values).toEqual([0, 0.5]);
  });

  it("比例轴上限取各期总和的最大值，至少为 1", () => {
    const low = buildPoolPeriods([result("r1", "2026-11-01T01:00:00Z", [member("a", { ratio: "0.2" })])]);
    expect(ratioAxisMax(low)).toBe(1);
    const high = buildPoolPeriods([
      result("r1", "2026-11-01T01:00:00Z", [member("a", { ratio: "0.2" })]),
      result("r2", "2026-11-01T02:00:00Z", [member("a", { ratio: "0.9" }), member("b", { ratio: "0.6" })])
    ]);
    expect(ratioAxisMax(high)).toBe(1.5);
  });

  it("会话切换处断开曲线，缺值也断开", () => {
    const periods = buildPoolPeriods([
      result("r1", "2026-11-01T01:00:00Z", [member("a")], { session_id: "s1" }),
      result("r2", "2026-11-01T02:00:00Z", [member("a")], { session_id: "s1" }),
      result("r3", "2026-11-01T03:00:00Z", [member("a")], { session_id: "s2" })
    ]);
    const breaks = sessionBreaks(periods);
    expect([...breaks]).toEqual([2]);
    expect(lineSegments([1, 1, 1], breaks)).toEqual([[0, 1], [2]]);
    expect(lineSegments([1, null, 1, 1])).toEqual([[0], [2, 3]]);
    expect(lineSegments([null, null])).toEqual([]);
  });

  it("净值范围包含基准 1 并留边距", () => {
    const range = equityRange([{ id: "a", values: [1.2, null, 0.9] }]);
    expect(range.min).toBeLessThan(0.9);
    expect(range.max).toBeGreaterThan(1.2);
    const flat = equityRange([{ id: "a", values: [1] }]);
    expect(flat.min).toBeLessThan(1);
    expect(flat.max).toBeGreaterThan(1);
    expect(equityRange([]).max).toBeGreaterThan(1);
  });
});

describe("颜色与数值", () => {
  it("20 个成员以上色板循环，同一成员的颜色固定", () => {
    expect(memberColor(0)).toBe(POOL_PALETTE[0]);
    expect(memberColor(POOL_PALETTE.length)).toBe(POOL_PALETTE[0]);
    expect(memberColor(19)).toBe(POOL_PALETTE[19 % POOL_PALETTE.length]);
    expect(memberColor(25)).toBe(POOL_PALETTE[25 % POOL_PALETTE.length]);
    expect(memberColor(-1)).toBe(POOL_PALETTE[0]);
    expect(new Set(POOL_PALETTE).size).toBe(POOL_PALETTE.length);
  });

  it("字符串比例转数字时去掉浮点尾巴，非法值返回 null", () => {
    expect(poolNumber("0.30000000000000004")).toBe(0.3);
    expect(poolNumber(0.1 + 0.2)).toBe(0.3);
    expect(poolNumber("-0.25")).toBe(-0.25);
    expect(poolNumber(" 1 ")).toBe(1);
    expect(poolNumber("")).toBeNull();
    expect(poolNumber("  ")).toBeNull();
    expect(poolNumber("abc")).toBeNull();
    expect(poolNumber(undefined)).toBeNull();
    expect(poolNumber(null)).toBeNull();
    expect(poolNumber(Number.NaN)).toBeNull();
    expect(poolNumber({})).toBeNull();
  });

  it("比例总和用定点口径相加，不出现 0.30000000000000004", () => {
    const periods = buildPoolPeriods([
      result("r1", "2026-11-01T01:00:00Z", [member("a", { ratio: "0.1" }), member("b", { ratio: "0.2" })])
    ]);
    expect(ratioTotals(periods)).toEqual([0.3]);
  });
});

// 第 6、7 期新增的池明细字段：drift 的分配系数、侧内再平衡与冻结，净敞口与总杠杆修正，条件转移，超限转移（摘要顶层的 limits）。
function driftPeriod(pool: Record<string, unknown>, extra: Record<string, unknown> = {}) {
  return parsePoolPeriod({
    result_id: "r",
    session_id: "s",
    bar_end_time: "2026-11-01T01:00:00Z",
    status: "ok",
    summary_json: JSON.stringify({
      pool: { method: "drift", members: [{ id: "a", status: "ok", ratio: "0.3" }], ...pool },
      ...extra
    })
  })!;
}

const drift = {
  phase: "drift",
  sides: [
    { side: "long", rebalance: "both", trigger: "threshold", deviation: 0.6512, moved: true, remaining: 2, reason: "threshold" },
    { side: "short", rebalance: "periodic", paused: true }
  ],
  freeze: { change: -0.1234, triggered: true, frozen: true, until: 1200 }
};

describe("策略池的漂移与组合约束明细", () => {
  it("解析成员的漂移后系数与合成比例，没有时为 null", () => {
    const parsed = driftPeriod({
      members: [
        { id: "a", status: "ok", ratio_before: "0.3", drifted: "0.31", ratio: "0.3", composed: "0.25" },
        { id: "b", status: "ok", ratio: "0.2" }
      ]
    });
    expect(parsed.members[0]).toMatchObject({ ratioBefore: 0.3, drifted: 0.31, ratio: 0.3, composed: 0.25 });
    expect(parsed.members[1]).toMatchObject({ drifted: null, composed: null });
  });

  it("解析 drift 的阶段、两侧再平衡与冻结", () => {
    const parsed = driftPeriod({ drift });
    expect(parsed.drift).toEqual({
      phase: "drift",
      sides: [
        {
          side: "long",
          rebalance: "both",
          trigger: "threshold",
          deviation: 0.6512,
          moved: true,
          remaining: 2,
          reason: "threshold",
          paused: false
        },
        {
          side: "short",
          rebalance: "periodic",
          trigger: "",
          deviation: null,
          moved: false,
          remaining: 0,
          reason: "",
          paused: true
        }
      ],
      freeze: { change: -0.1234, triggered: true, frozen: true, until: 1200 }
    });
    expect(poolPeriodNotes(parsed)).toEqual([
      { label: "漂移", text: "照常漂移" },
      {
        label: "多头侧",
        text: "定期与偏离超限再平衡，本根触发（偏离超限），最大偏离 65.12%，已移动侧内构成，分步还剩 2 根（偏离超限）"
      },
      { label: "空头侧", text: "定期再平衡，冻结中，分步暂停" },
      { label: "波动冻结", text: "窗口涨跌 -12.34%，本根触发，冻结中（到第 1200 根前）" }
    ]);
  });

  it("解析净敞口、总杠杆修正，条件转移与顶层的超限转移", () => {
    const parsed = driftPeriod(
      {
        method: "fixed",
        adjustments: [
          {
            kind: "exposure",
            band: 1,
            side: "long",
            factor: 0.5,
            long_before: 0.8,
            short_before: 0.2,
            long_after: 0.4,
            short_after: 0.2
          },
          {
            kind: "gross",
            side: "all",
            factor: 0.75,
            long_before: 0.8,
            short_before: 0.8,
            long_after: 0.6,
            short_after: 0.6,
            note: "要放大的一侧为 0"
          }
        ],
        switch: { index: 0, empty: "probe", from: "c", to: ["a"], moved: "0.5" }
      },
      {
        limits: [
          { instrument_id: "BTC-USDT", side: "long", excess: "0.1", round: 1, to: "c" },
          { instrument_id: "ETH-USDT", side: "short", excess: "0.02", round: 3 }
        ]
      }
    );
    expect(parsed.drift).toBeNull();
    expect(parsed.switch).toEqual({ index: 0, empty: "probe", from: "c", to: ["a"], moved: 0.5 });
    expect(parsed.adjustments[0]).toMatchObject({ kind: "exposure", band: 1, side: "long", factor: 0.5, longAfter: 0.4 });
    expect(parsed.limits).toEqual([
      { instrumentId: "BTC-USDT", side: "long", excess: 0.1, round: 1, to: "c" },
      { instrumentId: "ETH-USDT", side: "short", excess: 0.02, round: 3, to: "" }
    ]);
    expect(poolPeriodNotes(parsed)).toEqual([
      { label: "净敞口第 1 段", text: "多头 ×0.5（多头 80.00% → 40.00%，空头 20.00% → 20.00%）" },
      { label: "总杠杆", text: "整体 ×0.75（多头 80.00% → 60.00%，空头 80.00% → 60.00%）；要放大的一侧为 0" },
      { label: "条件转移", text: "probe 为空：c 的 50.00% 转给 a" },
      { label: "超限转移", text: "BTC-USDT 多头超出 10.00%（第 1 轮）→ c" },
      { label: "超限转移", text: "ETH-USDT 空头超出 2.00%（第 3 轮）→ 留现金" }
    ]);
  });

  it("没写这些字段时为空，不出说明", () => {
    const parsed = driftPeriod({ method: "fixed" });
    expect(parsed).toMatchObject({ drift: null, adjustments: [], switch: null, limits: [] });
    expect(poolPeriodNotes(parsed)).toEqual([]);
  });

  it("写错类型的字段按没写处理", () => {
    const parsed = driftPeriod(
      {
        drift: { phase: 1, sides: "long" },
        adjustments: "exposure",
        switch: { from: 3 }
      },
      { limits: [null, "x"] }
    );
    expect(parsed).toMatchObject({ drift: null, adjustments: [], switch: null, limits: [] });
    const sides = driftPeriod({
      drift: { phase: "start", sides: [null, { side: "long", remaining: 1.5, deviation: "x" }], freeze: "on" }
    }).drift!;
    expect(sides.sides).toEqual([
      { side: "long", rebalance: "", trigger: "", deviation: null, moved: false, remaining: 0, reason: "", paused: false }
    ]);
    expect(sides.freeze).toBeNull();
  });

  it("parsePoolSummary 直接读已解析的摘要对象（回放的 decision 用它）", () => {
    const source = { result_id: "x", session_id: "replay", bar_end_time: "2026-11-01T02:00:00Z", status: "skipped" };
    const parsed = parsePoolSummary({ pool: { method: "drift", members: [{ id: "a" }], drift }, limits: [] }, source)!;
    expect(parsed).toMatchObject({ resultId: "x", sessionId: "replay", resultStatus: "skipped", method: "drift" });
    expect(parsed.drift?.phase).toBe("drift");
    expect(parsePoolSummary(undefined, source)).toBeNull();
    expect(parsePoolSummary({ pool: { members: [{ id: "a" }] } }, { ...source, bar_end_time: "坏的时间" })).toBeNull();
  });
});
