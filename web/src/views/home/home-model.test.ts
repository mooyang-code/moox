import { describe, expect, it } from "vitest";
import type { FactorSetInfo } from "@/api/factor/types";
import type { HealthOverview } from "@/api/monitor";
import type { StrategyInstance, StrategyTargetSnapshot } from "@/api/strategy-types";
import {
  TILE_LIMITS,
  activityRows,
  attentionItems,
  bannerFacts,
  clockTime,
  factorRows,
  fold,
  freshnessRows,
  hostRows,
  humanizeDuration,
  kpiCards,
  prettifyIds,
  relativeTime,
  shortTime,
  statusLine,
  strategyRows,
  systemStage,
  toSeconds,
  toneIcon,
  toneOf
} from "./home-model";

const NOW = Date.parse("2026-10-13T10:00:00");
const names = { datasets: { dataset_abc: "现货K线" }, views: { view_xyz: "因子宽表" } };

function instance(id: string, overrides: Partial<StrategyInstance> = {}): StrategyInstance {
  return {
    instance_id: id,
    strategy_id: "s1",
    space_id: "crypto",
    view_id: "v",
    logical_account_id: "",
    enabled: true,
    session_id: "sess",
    resolved_json: "{}",
    health: "ok",
    created_at: "",
    updated_at: "2026-10-13T00:00:00Z",
    ...overrides
  };
}
function snapshot(validUntil: string, weights: Record<string, string>): StrategyTargetSnapshot {
  return {
    targets: Object.entries(weights).map(([instrument_id, target_weight]) => ({ instrument_id, target_weight })),
    session_id: "sess",
    bar_end_time: new Date(NOW - 120_000).toISOString(),
    valid_until: validUntil,
    result_id: "r"
  };
}

describe("home formatting", () => {
  it("humanizes durations in Chinese", () => {
    expect(humanizeDuration(null)).toBe("—");
    expect(humanizeDuration(42)).toBe("42 秒");
    expect(humanizeDuration(95)).toBe("1 分钟");
    expect(humanizeDuration(3600)).toBe("1 小时");
    expect(humanizeDuration(51035)).toBe("14 小时 10 分");
    expect(humanizeDuration(90000)).toBe("1.0 天");
    expect(toSeconds("51035")).toBe(51035);
    expect(toSeconds("")).toBeNull();
  });

  it("writes relative and short times", () => {
    expect(relativeTime(new Date(NOW - 2_000).toISOString(), NOW)).toBe("刚刚");
    expect(relativeTime(new Date(NOW - 10_000).toISOString(), NOW)).toBe("10 秒前");
    expect(relativeTime(new Date(NOW - 5 * 60_000).toISOString(), NOW)).toBe("5 分钟前");
    expect(relativeTime(new Date(NOW - 3 * 3600_000).toISOString(), NOW)).toBe("3 小时前");
    expect(relativeTime(undefined, NOW)).toBe("—");
    expect(shortTime(new Date(NOW - 3600_000).toISOString(), NOW)).toBe("09:00");
    expect(shortTime("2026-10-11T08:05:00", NOW)).toBe("10-11 08:05");
    expect(shortTime(String(Math.floor((NOW - 60_000) / 1000)), NOW)).toBe("09:59");
  });

  it("replaces internal dataset and view ids with names", () => {
    expect(prettifyIds("dataset_abc 1h：新增 1 个；view_xyz 已刷新；dataset_zzz 未知", names)).toBe(
      "现货K线 1h：新增 1 个；因子宽表 已刷新；dataset_zzz 未知"
    );
  });

  it("maps monitor statuses to tones", () => {
    expect([toneOf("healthy"), toneOf("degraded"), toneOf("down"), toneOf("unknown")]).toEqual(["ok", "warn", "bad", "idle"]);
  });
});

describe("home data rows", () => {
  it("lists freshness worst first with named datasets and readable lag", () => {
    const overview: HealthOverview = {
      pipeline: [
        {
          name: "存储",
          datasets: [
            { producer: "storage", dataset_id: "dataset_abc", freq: "1h", status: "healthy" },
            {
              producer: "storage",
              dataset_id: "dataset_abc",
              freq: "1m",
              status: "degraded",
              waiting_bars: 850,
              overdue_seconds: "51035",
              reason: "落后"
            }
          ]
        },
        {
          name: "视图",
          datasets: [{ producer: "storage_view", dataset_id: "view_xyz", freq: "1m", status: "healthy" }]
        }
      ]
    };
    const rows = freshnessRows(overview, names);
    // 应收的最新一根已到写“已追平”；还没到的写从它收线起已等待多久（这里没有收线时刻，用服务端给的等待时长）。
    expect(rows.map(row => [row.stage, row.name, row.freq, row.lag, row.status])).toEqual([
      ["入库", "现货K线", "1m", "等待 14 小时 10 分", "需关注"],
      ["入库", "现货K线", "1h", "已追平", "正常"],
      ["视图", "因子宽表", "1m", "已追平", "正常"]
    ]);
  });

  it("summarizes factor tasks of the current space with health and lag", () => {
    const sets = [
      {
        factor_set: { set_id: "a", space_id: "crypto", source_dataset_id: "dataset_abc", freq: "1m", status: "enabled" },
        members: [{ status: "enabled" }, { status: "enabled" }, { status: "disabled" }],
        last_run: {
          set_id: "a",
          last_period_time: String(Math.floor((NOW - 60_000) / 1000)),
          last_status: "ok",
          lag_seconds: "8"
        }
      },
      {
        factor_set: { set_id: "b", space_id: "crypto", source_dataset_id: "dataset_abc", freq: "1m", status: "enabled" },
        members: [],
        last_run: { set_id: "b", last_period_time: "1791900000", last_status: "failed", lag_seconds: "600" }
      },
      { factor_set: { set_id: "c", space_id: "other", source_dataset_id: "x", freq: "1h", status: "enabled" }, members: [] }
    ] as unknown as FactorSetInfo[];
    const rows = factorRows(sets, "crypto", names, NOW);
    expect(rows.map(row => [row.setId, row.label, row.factors, row.health.label, row.tone])).toEqual([
      ["b", "现货K线 · 1m", 0, "失败", "bad"],
      ["a", "现货K线 · 1m", 2, "正常", "ok"]
    ]);
    expect(rows[1].lastPeriod).toBe("09:59");
    expect(rows[1].lag).toBe("已追平");
    expect(rows[1].behindSeconds).toBe(0);
    expect(rows[0].behindSeconds).toBeGreaterThan(0);
  });

  it("shows what each strategy wants to hold, largest weights first", () => {
    const later = new Date(NOW + 60_000).toISOString();
    const rows = strategyRows(
      [instance("a"), instance("off", { enabled: false, session_id: "" })],
      { a: snapshot(later, { "ETH-USDT": "0.1", "BTC-USDT": "0.2", "SOL-USDT": "-0.15" }) },
      [{ strategy_id: "s1", name: "动量" }] as never,
      NOW
    );
    expect(rows[0]).toMatchObject({ name: "动量", tone: "ok", lastBar: "2 分钟前", more: 0 });
    expect(rows[0].state.label).toBe("目标有效");
    expect(rows[0].holdings.map(item => `${item.id} ${item.weight}${item.short ? " 空" : ""}`)).toEqual([
      "BTC-USDT 20.0%",
      "SOL-USDT -15.0% 空",
      "ETH-USDT 10.0%"
    ]);
    expect(rows[1]).toMatchObject({ enabled: false, tone: "idle", lastBar: "—" });
    expect(rows[1].state.label).toBe("已停用");
  });

  it("merges monitor alerts, strategy and factor problems with the severe ones first", () => {
    const items = attentionItems({
      alerts: [
        {
          id: "a1",
          severity: "warning",
          title: "dataset_abc 延迟",
          reason: "超过 2 个周期",
          triggered_at: new Date(NOW - 600_000).toISOString()
        }
      ],
      strategy: {
        instances: [instance("deg", { health: "degraded" })],
        targets: {},
        catalog: [{ strategy_id: "s1", name: "动量" }] as never
      },
      freshness: [],
      factors: [
        {
          setId: "f",
          label: "现货K线 · 1m",
          factors: 2,
          health: { key: "lagging", label: "滞后", color: "red" },
          tone: "warn",
          lastPeriod: "09:00",
          lag: "1 小时",
          path: "/factor"
        }
      ],
      names,
      nowMs: NOW,
      alertTarget: () => "/ops/monitor"
    });
    expect(items.map(item => [item.source, item.tone, item.title])).toEqual([
      ["策略", "bad", "动量 · deg"],
      ["监控", "warn", "现货K线 延迟"],
      ["因子", "warn", "现货K线 · 1m"]
    ]);
    expect(items[1].when).toBe("10 分钟前");
  });

  it("flags hot hosts and formats resource percentages", () => {
    const [host] = hostRows([
      {
        host_id: "h",
        hostname: "vm-1",
        status: "healthy",
        cpu_available: true,
        cpu_percent: 91.2,
        memory_available: true,
        memory_percent: 30,
        disk_available: false
      }
    ]);
    expect(host).toMatchObject({
      name: "vm-1",
      tone: "warn",
      cpu: "91%",
      memory: "30%",
      disk: "—",
      hot: { cpu: true, memory: false, disk: false }
    });
  });

  it("labels activity categories and names datasets in titles", () => {
    const [row] = activityRows(
      [
        {
          id: 1,
          category: "collector.subject_change",
          level: "info",
          title: "dataset_abc 1h：新增 1 个",
          created_at: new Date(NOW - 3600_000).toISOString()
        }
      ],
      names,
      NOW
    );
    expect(row).toMatchObject({ when: "09:00", category: "标的变更", title: "现货K线 1h：新增 1 个", tone: "idle" });
  });

  it("states the overall situation in one line", () => {
    expect(statusLine({ attention: [], overview: {}, monitorError: "" })).toEqual({ tone: "ok", text: "运行正常" });
    expect(statusLine({ attention: [], overview: undefined, monitorError: "boom" }).text).toBe("监控数据暂不可用");
    const warn = { key: "k", tone: "warn" as const, source: "", title: "", detail: "", when: "", target: "/" };
    expect(statusLine({ attention: [warn, { ...warn, tone: "bad" }], overview: {}, monitorError: "" })).toEqual({
      tone: "bad",
      text: "2 项需要处理"
    });
  });
});

describe("home tiles", () => {
  const later = new Date(NOW + 60_000).toISOString();
  const catalog = [{ strategy_id: "s1", name: "动量" }] as never;

  it("draws weight bars relative to the largest bar, cash included", () => {
    const [row] = strategyRows([instance("a")], { a: snapshot(later, { "BTC-USDT": "0.2", "ETH-USDT": "0.1" }) }, catalog, NOW);
    expect(row.gross).toBeCloseTo(0.3);
    expect(row.cash).toEqual({ text: "70.0%", ratio: 1 });
    expect(row.holdings.map(item => item.ratio)).toEqual([0.2 / 0.7, 0.1 / 0.7]);
    expect(row.holdingCount).toBe(2);
  });

  it("folds holdings beyond the tile limit into a count", () => {
    const weights = Object.fromEntries(Array.from({ length: 7 }, (_, i) => [`T${i}`, String(0.1 - i * 0.001)]));
    const [row] = strategyRows([instance("a")], { a: snapshot(later, weights) }, catalog, NOW);
    expect(row.holdings).toHaveLength(4);
    expect(row.more).toBe(3);
    expect(row.holdingCount).toBe(7);
  });

  it("folds list tiles to a fixed number of rows", () => {
    expect(fold([1, 2, 3, 4, 5, 6, 7], TILE_LIMITS.attention)).toEqual({ shown: [1, 2, 3, 4, 5], hidden: 2 });
    expect(fold([1, 2], TILE_LIMITS.attention)).toEqual({ shown: [1, 2], hidden: 0 });
  });

  it("maps tones to status icons", () => {
    expect([toneIcon("ok"), toneIcon("warn"), toneIcon("bad"), toneIcon("idle")]).toEqual([
      "icon-check-circle-fill",
      "icon-exclamation-circle-fill",
      "icon-close-circle-fill",
      "icon-minus-circle-fill"
    ]);
  });

  it("summarizes the three banner facts with the worst tone", () => {
    const strategies = strategyRows([instance("a")], { a: snapshot(later, { "BTC-USDT": "0.2" }) }, catalog, NOW);
    const factors = factorRows(
      [
        {
          factor_set: { set_id: "a", space_id: "crypto", source_dataset_id: "d", freq: "1m", status: "enabled" },
          members: [],
          last_run: {
            set_id: "a",
            last_period_time: String(Math.floor((NOW - 120_000) / 1000)),
            last_status: "ok",
            lag_seconds: "8"
          }
        }
      ] as unknown as FactorSetInfo[],
      "crypto",
      names,
      NOW
    );
    const freshness = freshnessRows(
      {
        pipeline: [
          {
            name: "存储",
            datasets: [
              {
                producer: "storage",
                dataset_id: "dataset_abc",
                freq: "1m",
                status: "degraded",
                latest_bar_close_at: new Date(NOW - 51035_000).toISOString(),
                waiting_bars: 850,
                overdue_seconds: "51000"
              },
              {
                producer: "storage",
                dataset_id: "dataset_abc",
                freq: "1h",
                status: "healthy",
                latest_bar_close_at: new Date(NOW - 3600_000).toISOString()
              }
            ]
          }
        ]
      },
      names,
      NOW
    );
    const facts = bannerFacts({ strategies, factors, freshness, nowMs: NOW });
    expect(facts.map(fact => [fact.key, fact.value, fact.tone])).toEqual([
      ["strategy", "09:58:00", "ok"],
      ["factor", "09:59:00", "ok"],
      ["data", "10-12 19:49:25", "warn"]
    ]);
    expect(bannerFacts({ strategies: [], factors: [], freshness: [], nowMs: NOW }).map(fact => fact.value)).toEqual([
      "—",
      "—",
      "—"
    ]);
  });

  it("writes the exact close time to the second and adds the date for older bars", () => {
    const today = new Date(NOW - 125_000);
    expect(clockTime(today.toISOString(), NOW)).toBe("09:57:55");
    expect(clockTime(String(Math.floor((NOW - 86_400_000 - 3_600_000) / 1000)), NOW)).toBe("10-12 09:00:00");
    expect(clockTime(null, NOW)).toBe("—");
    expect(clockTime("bad", NOW)).toBe("—");
  });

  it("marks an expired strategy and shows its bar even when another instance is fresher", () => {
    const valid = new Date(NOW + 60_000).toISOString();
    const expired = new Date(NOW - 60_000).toISOString();
    const fresh = { ...snapshot(valid, { "BTC-USDT": "0.2" }), bar_end_time: new Date(NOW - 60_000).toISOString() };
    const stale = { ...snapshot(expired, { "BTC-USDT": "0.2" }), bar_end_time: new Date(NOW - 3_600_000).toISOString() };
    const strategies = strategyRows([instance("a"), instance("b")], { a: fresh, b: stale }, catalog, NOW);
    const facts = bannerFacts({ strategies, factors: [], freshness: [], nowMs: NOW });
    expect(facts[0]).toMatchObject({ key: "strategy", value: "09:00:00", tone: "warn" });
    const healthy = strategyRows([instance("a")], { a: fresh }, catalog, NOW);
    expect(bannerFacts({ strategies: healthy, factors: [], freshness: [], nowMs: NOW })[0]).toMatchObject({
      value: "09:59:00",
      tone: "ok"
    });
  });

  it("shows the factor task that is furthest behind, otherwise the freshest close time", () => {
    const set = (id: string, freq: string, openedSecondsAgo: number, status = "ok") => ({
      factor_set: { set_id: id, space_id: "crypto", source_dataset_id: "dataset_abc", freq, status: "enabled" },
      members: [],
      last_run: {
        set_id: id,
        last_period_time: String(Math.floor((NOW - openedSecondsAgo * 1000) / 1000)),
        last_status: status,
        // 汇总的“现在”是 上一期时间 + lag_seconds：这里就是服务端读取时距上一期开盘过了多久。
        lag_seconds: String(openedSecondsAgo)
      }
    });
    const facts = (sets: unknown[]) =>
      bannerFacts({
        strategies: [],
        factors: factorRows(sets as FactorSetInfo[], "crypto", names, NOW),
        freshness: [],
        nowMs: NOW
      })[1];
    // 1m 刚追平（09:58 开盘，09:59:00 收盘）；1h 的上一期是 08:00 开盘、09:00:00 收盘，也已追平：都正常时取最新的收盘时间。
    expect(facts([set("a", "1m", 120), set("h", "1h", 7200)])).toMatchObject({ value: "09:59:00", tone: "ok" });
    // 1m 已经 30 分钟没有产出：滞后的那个任务优先，时间是它自己的收盘时间。
    expect(facts([set("a", "1m", 120), set("b", "1m", 1860)])).toMatchObject({ value: "09:30:00", tone: "warn" });
    // 失败的任务比滞后的更靠前，并标红。
    expect(facts([set("a", "1m", 1860), set("f", "1m", 300, "failed")])).toMatchObject({ value: "09:56:00", tone: "bad" });
  });

  it("builds four stable KPI cards", () => {
    const strategies = strategyRows(
      [instance("a"), instance("off", { enabled: false })],
      { a: snapshot(later, { "BTC-USDT": "0.2", "ETH-USDT": "0.2" }) },
      catalog,
      NOW
    );
    const freshness = freshnessRows(
      {
        pipeline: [
          {
            name: "存储",
            datasets: [
              {
                producer: "storage",
                dataset_id: "d1",
                freq: "1m",
                status: "degraded",
                latest_bar_close_at: new Date(NOW - 840_000).toISOString(),
                waiting_bars: 14,
                overdue_seconds: "780"
              },
              {
                producer: "storage",
                dataset_id: "d2",
                freq: "1m",
                status: "healthy",
                latest_bar_close_at: new Date(NOW - 5_000).toISOString()
              }
            ]
          }
        ]
      },
      names,
      NOW
    );
    const cards = kpiCards({
      strategies,
      strategyLoaded: true,
      factors: [],
      factorLoaded: true,
      freshness,
      alertCount: 0,
      monitorError: "",
      nowMs: NOW
    });
    expect(cards.map(card => [card.key, card.value, card.unit, card.note, card.tone])).toEqual([
      ["strategy", "1", " / 1 有效", "2 个标的 · 40% 仓位", "ok"],
      ["factor", "0", " 个任务", "0 个因子 · 收盘 —", "idle"],
      ["freshness", "1", " / 2 正常", "1 条落后 · 收盘 09:46:00", "warn"],
      ["alerts", "0", "", "组件、数据与主机", "idle"]
    ]);
  });

  it("writes the close time on the factor and freshness cards and keeps the abnormal count", () => {
    const set = (id: string, openedSecondsAgo: number, status = "ok") => ({
      factor_set: { set_id: id, space_id: "crypto", source_dataset_id: "dataset_abc", freq: "1m", status: "enabled" },
      members: [{ status: "enabled" }, { status: "enabled" }],
      last_run: {
        set_id: id,
        last_period_time: String(Math.floor((NOW - openedSecondsAgo * 1000) / 1000)),
        last_status: status,
        lag_seconds: String(openedSecondsAgo)
      }
    });
    const cardsOf = (sets: unknown[], datasets: unknown[]) =>
      kpiCards({
        strategies: [],
        strategyLoaded: true,
        factors: factorRows(sets as FactorSetInfo[], "crypto", names, NOW),
        factorLoaded: true,
        freshness: freshnessRows(
          { generated_at: new Date(NOW).toISOString(), pipeline: [{ name: "存储", datasets }] } as never,
          names,
          NOW
        ),
        alertCount: 0,
        monitorError: "",
        nowMs: NOW
      });
    const at = (secondsAgo: number) => new Date(NOW - secondsAgo * 1000).toISOString();
    const healthy = cardsOf(
      [set("a", 120)],
      [
        {
          producer: "storage",
          dataset_id: "dataset_abc",
          freq: "1m",
          status: "healthy",
          output_watermark_at: at(120),
          latest_bar_close_at: at(60)
        }
      ]
    );
    expect(healthy[1]).toMatchObject({ note: "2 个因子 · 收盘 09:59:00", tone: "ok" });
    expect(healthy[2]).toMatchObject({ note: "收盘 09:59:00", tone: "ok" });
    const bad = cardsOf(
      [set("a", 120), set("b", 1860, "failed")],
      [
        {
          producer: "storage",
          dataset_id: "dataset_abc",
          freq: "1m",
          status: "healthy",
          output_watermark_at: at(120),
          latest_bar_close_at: at(60)
        },
        {
          producer: "storage",
          dataset_id: "view_xyz",
          freq: "1m",
          status: "degraded",
          output_watermark_at: at(7200),
          latest_bar_close_at: at(7140),
          waiting_bars: 119,
          waiting_since: at(7080)
        }
      ]
    );
    // 异常的任务 / 数据集排在前面：卡片上的时间就是它们自己的收盘时间，不会被正常的覆盖。
    expect(bad[1]).toMatchObject({ note: "1 个异常 · 收盘 09:30:00", tone: "warn" });
    expect(bad[2]).toMatchObject({ note: "1 条落后 · 收盘 08:01:00", tone: "warn" });
    expect(bad[1].hint).toContain("收盘时间");
    expect(bad[2].hint).toContain("收盘时间");
  });

  it("keeps the KPI cards honest when monitoring is down", () => {
    const cards = kpiCards({
      strategies: [],
      strategyLoaded: false,
      factors: [],
      factorLoaded: false,
      freshness: [],
      alertCount: undefined,
      monitorError: "boom",
      nowMs: NOW
    });
    expect(cards.map(card => card.value)).toEqual(["—", "—", "—", "—"]);
    expect(cards[2].note).toBe("监控数据暂不可用");
  });

  it("never shows a zero delay for a dataset whose status is unknown, and lists it after the healthy ones", () => {
    const rows = freshnessRows(
      {
        pipeline: [
          {
            name: "采集",
            datasets: [
              { producer: "collector", dataset_id: "dataset_abc", freq: "1h", status: "unknown" },
              {
                producer: "storage",
                dataset_id: "dataset_abc",
                freq: "1m",
                status: "healthy",
                latest_bar_close_at: new Date(NOW - 60_000).toISOString()
              },
              {
                producer: "storage",
                dataset_id: "dataset_abc",
                freq: "1h",
                status: "degraded",
                latest_bar_close_at: new Date(NOW - 3600_000).toISOString(),
                waiting_bars: 1,
                waiting_since: new Date(NOW - 900_000).toISOString()
              }
            ]
          }
        ]
      },
      names,
      NOW
    );
    expect(rows.map(row => [row.freq, row.lag, row.tone])).toEqual([
      ["1h", "等待 15 分钟", "warn"],
      ["1m", "已追平", "ok"],
      ["1h", "—", "idle"]
    ]);
    const facts = bannerFacts({ strategies: [], factors: [], freshness: rows, nowMs: NOW });
    // 横幅写 1h 数据集已到的最新一根的收线时间 09:00（后端按交易日历算出）；它在等待 10:00 前缺的那根，所以换成告警色。
    expect(facts[2]).toMatchObject({ value: "09:00:00", tone: "warn" });
  });

  it("counts stale data as something to handle, without repeating what a monitor alert already covers", () => {
    const freshness = freshnessRows(
      {
        pipeline: [
          {
            name: "存储",
            datasets: [
              {
                producer: "storage",
                dataset_id: "dataset_abc",
                freq: "1m",
                status: "degraded",
                waiting_bars: 882,
                overdue_seconds: "52920"
              },
              {
                producer: "storage",
                dataset_id: "dataset_abc",
                freq: "1h",
                status: "degraded",
                waiting_bars: 5,
                overdue_seconds: "18660"
              }
            ]
          }
        ]
      },
      names
    );
    const base = {
      strategy: { instances: [], targets: {}, catalog: [] },
      factors: [],
      freshness,
      names,
      nowMs: NOW,
      alertTarget: () => "/ops/monitor"
    };
    const items = attentionItems({ ...base, alerts: [] });
    expect(items.map(item => [item.source, item.title, item.detail])).toEqual([
      ["数据", "入库 · 现货K线 · 1m", "等待 14 小时 42 分"],
      ["数据", "入库 · 现货K线 · 1h", "等待 5 小时 11 分"]
    ]);
    expect(statusLine({ attention: items, overview: {}, monitorError: "" })).toEqual({ tone: "warn", text: "2 项需要处理" });
    const covered = attentionItems({
      ...base,
      alerts: [
        { id: "a", severity: "warning", title: "入库落后", object: { type: "dataset", dataset_id: "dataset_abc", freq: "1m" } }
      ]
    });
    expect(covered.map(item => item.source)).toEqual(["监控", "数据"]);
  });

  it("drops no-change subject events and shortens the effective time in activity titles", () => {
    const rows = activityRows(
      [
        {
          id: 1,
          category: "collector.subject_change",
          level: "info",
          title: "dataset_abc 1h：新增 0 个、移除 0 个标的，将于 2026-10-12 13:00 生效",
          created_at: new Date(NOW - 3600_000).toISOString()
        },
        {
          id: 2,
          category: "collector.subject_change",
          level: "info",
          title: "dataset_abc 1h：新增 1 个、移除 0 个标的，将于 2026-10-13 09:00 生效",
          created_at: new Date(NOW - 7200_000).toISOString()
        }
      ],
      names,
      NOW
    );
    expect(rows.map(row => row.title)).toEqual(["现货K线 1h：新增 1 个、移除 0 个标的，10-13 09:00 生效"]);
  });

  it("reads caught-up and waiting from the backend's calendar-aware latest bar, never from now minus the watermark", () => {
    const at = (secondsBefore: number) => new Date(NOW - secondsBefore * 1000).toISOString();
    const rows = freshnessRows(
      {
        generated_at: new Date(NOW).toISOString(),
        pipeline: [
          {
            name: "存储",
            datasets: [
              // 应收的最新一根已到：已追平，尽管“现在 − 水位”是 2 分钟。
              {
                producer: "storage",
                dataset_id: "dataset_abc",
                freq: "1m",
                status: "healthy",
                output_watermark_at: at(120),
                latest_bar_close_at: at(60)
              },
              // 水位停在 09:55：09:56 那根 09:57 收线后还没到，按服务端时钟已等待 3 分钟（缺 3 根）。
              {
                producer: "storage",
                dataset_id: "view_xyz",
                freq: "1m",
                status: "degraded",
                output_watermark_at: at(300),
                latest_bar_close_at: at(240),
                waiting_bars: 3,
                waiting_since: at(180)
              }
            ]
          }
        ]
      },
      names
    );
    expect(rows.map(row => [row.name, row.lag, row.seconds])).toEqual([
      ["因子宽表", "等待 3 分钟", 180],
      ["现货K线", "已追平", 0]
    ]);
    const facts = bannerFacts({ strategies: [], factors: [], freshness: rows, nowMs: NOW });
    // 横幅写已到的最新一根的收线时间，并按状态换色。
    expect(facts[2]).toMatchObject({ value: "09:56:00", tone: "warn" });
  });

  it('reads "caught up" for factors whose last period was computed on time', () => {
    const sets = (lagSeconds: string) =>
      [
        {
          factor_set: { set_id: "a", space_id: "crypto", source_dataset_id: "d", freq: "1m", status: "enabled" },
          members: [],
          last_run: { set_id: "a", last_period_time: "1791900000", last_status: "ok", lag_seconds: lagSeconds }
        }
      ] as unknown as FactorSetInfo[];
    // 上一期开盘后 70 秒、125 秒仍是同一期：1m K 线收盘后才产出，这两个时刻都不是“滞后 1～2 分钟”。
    for (const lag of ["70", "125"])
      expect(factorRows(sets(lag), "crypto", names, NOW)[0]).toMatchObject({ lag: "已追平", tone: "ok" });
    expect(factorRows(sets("400"), "crypto", names, NOW)[0]).toMatchObject({ lag: "落后 5 分钟", tone: "warn" });
  });
});

describe("systemStage", () => {
  it("没有模块上报生命周期状态时不显示", () => {
    expect(systemStage(undefined)).toBeUndefined();
    expect(systemStage({} as HealthOverview)).toBeUndefined();
  });

  it("按运行阶段显示名称与颜色", () => {
    expect(systemStage({ system: { phase: "running", color: "green", health: "healthy" } })).toEqual({
      color: "green",
      label: "运行中",
      issues: [],
      recovered: false
    });
    expect(systemStage({ system: { phase: "warming", color: "yellow", health: "healthy" } })?.label).toBe("预热中");
    expect(systemStage({ system: { phase: "ready", color: "blue", health: "healthy" } })?.label).toBe("就绪");
  });

  it("降级与健康面异常分别提示，并列出原因", () => {
    const degraded = systemStage({
      system: {
        phase: "running",
        color: "orange",
        degraded: true,
        health: "healthy",
        issues: ["因子引擎：crypto_1m 变更同步：变更日志断档"]
      }
    });
    expect(degraded?.label).toBe("运行中 · 降级");
    expect(degraded?.issues).toEqual(["因子引擎：crypto_1m 变更同步：变更日志断档"]);

    const bad = systemStage({ system: { phase: "running", color: "red", health: "attention", issues: ["Trade：数据库不可用"] } });
    expect(bad?.label).toBe("运行中 · 有模块未就绪");
    expect(bad?.color).toBe("red");
  });

  it("暂停、部分暂停与重置中", () => {
    const paused = systemStage({
      system: {
        phase: "paused",
        color: "orange",
        health: "healthy",
        issues: ["策略：系统暂停：切换行情源"],
        pause_kinds: { system: 2 }
      }
    });
    expect(paused).toEqual({ color: "orange", label: "已暂停", issues: ["策略：系统暂停：切换行情源"], recovered: false });
    expect(systemStage({ system: { phase: "partially_paused", color: "orange", health: "healthy" } })?.label).toBe("部分暂停");
    const resetting = systemStage({ system: { phase: "resetting", color: "purple", health: "healthy" } });
    expect(resetting?.label).toBe("重置中");
    expect(resetting?.color).toBe("purple");
  });

  it("恢复追赶：显示追平进度与各模块说明，全部追平后提示待放行", () => {
    const resuming = systemStage({
      system: {
        phase: "resuming",
        color: "cyan",
        health: "healthy",
        resuming_modules: 3,
        caught_up_modules: 1,
        catchup_notes: ["因子引擎：补采 6/10", "策略：被 factor_set/crypto_1m 阻塞"]
      }
    });
    expect(resuming?.label).toBe("恢复中 · 追赶 1/3 个模块");
    expect(resuming?.color).toBe("cyan");
    expect(resuming?.issues).toEqual(["因子引擎：补采 6/10", "策略：被 factor_set/crypto_1m 阻塞"]);
    const done = systemStage({
      system: { phase: "resuming", color: "cyan", health: "healthy", resuming_modules: 3, caught_up_modules: 3 }
    });
    expect(done?.label).toBe("恢复中 · 已追平，待放行");
  });

  it("原因最多显示 3 条，其余折叠；自动恢复要提示", () => {
    const stage = systemStage({
      system: { phase: "running", color: "orange", degraded: true, auto_recovered: true, issues: ["a", "b", "c", "d", "e"] }
    });
    expect(stage?.issues).toEqual(["a", "b", "c", "另有 2 项"]);
    expect(stage?.recovered).toBe(true);
  });
});
