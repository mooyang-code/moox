import { describe, expect, it } from "vitest";
import type { FactorSetInfo } from "@/api/factor/types";
import type { HealthOverview } from "@/api/monitor";
import type { StrategyInstance, StrategyTargetSnapshot } from "@/api/strategy-types";
import {
  TILE_LIMITS,
  activityRows,
  attentionItems,
  bannerFacts,
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
            { producer: "storage", dataset_id: "dataset_abc", freq: "1h", status: "healthy", lag_seconds: "95" },
            {
              producer: "storage",
              dataset_id: "dataset_abc",
              freq: "1m",
              status: "degraded",
              lag_seconds: "51035",
              reason: "落后"
            }
          ]
        },
        {
          name: "视图",
          datasets: [{ producer: "storage_view", dataset_id: "view_xyz", freq: "1m", status: "healthy", lag_seconds: 4535 }]
        }
      ]
    };
    const rows = freshnessRows(overview, names);
    expect(rows.map(row => [row.stage, row.name, row.freq, row.lag, row.status])).toEqual([
      ["入库", "现货K线", "1m", "14 小时 10 分", "需关注"],
      ["视图", "因子宽表", "1m", "1 小时 15 分", "正常"],
      ["入库", "现货K线", "1h", "1 分钟", "正常"]
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
    expect(rows[1].lag).toBe("8 秒");
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
          last_run: { set_id: "a", last_period_time: "1791900000", last_status: "ok", lag_seconds: "8" }
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
              { producer: "storage", dataset_id: "dataset_abc", freq: "1m", status: "degraded", lag_seconds: "51035" },
              { producer: "storage", dataset_id: "dataset_abc", freq: "1h", status: "healthy", lag_seconds: "95" }
            ]
          }
        ]
      },
      names
    );
    const facts = bannerFacts({ strategies, factors, freshness, nowMs: NOW });
    expect(facts.map(fact => [fact.key, fact.value, fact.tone])).toEqual([
      ["strategy", "2 分钟前", "ok"],
      ["factor", "8 秒", "ok"],
      ["data", "14 小时 10 分", "warn"]
    ]);
    expect(bannerFacts({ strategies: [], factors: [], freshness: [], nowMs: NOW }).map(fact => fact.value)).toEqual([
      "—",
      "—",
      "—"
    ]);
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
              { producer: "storage", dataset_id: "d1", freq: "1m", status: "degraded", lag_seconds: "900" },
              { producer: "storage", dataset_id: "d2", freq: "1m", status: "healthy", lag_seconds: "5" }
            ]
          }
        ]
      },
      names
    );
    const cards = kpiCards({
      strategies,
      strategyLoaded: true,
      factors: [],
      factorLoaded: true,
      freshness,
      alertCount: 0,
      monitorError: ""
    });
    expect(cards.map(card => [card.key, card.value, card.unit, card.note, card.tone])).toEqual([
      ["strategy", "1", " / 1 有效", "2 个标的 · 40% 仓位", "ok"],
      ["factor", "0", " 个任务", "0 个因子 · 全部正常", "idle"],
      ["freshness", "1", " / 2 正常", "1 条落后", "warn"],
      ["alerts", "0", "", "组件、数据与主机", "idle"]
    ]);
  });

  it("keeps the KPI cards honest when monitoring is down", () => {
    const cards = kpiCards({
      strategies: [],
      strategyLoaded: false,
      factors: [],
      factorLoaded: false,
      freshness: [],
      alertCount: undefined,
      monitorError: "boom"
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
              { producer: "collector", dataset_id: "dataset_abc", freq: "1h", status: "unknown", lag_seconds: "0" },
              { producer: "storage", dataset_id: "dataset_abc", freq: "1m", status: "healthy", lag_seconds: "5" },
              { producer: "storage", dataset_id: "dataset_abc", freq: "1h", status: "degraded", lag_seconds: "900" }
            ]
          }
        ]
      },
      names
    );
    expect(rows.map(row => [row.freq, row.lag, row.tone])).toEqual([
      ["1h", "15 分钟", "warn"],
      ["1m", "5 秒", "ok"],
      ["1h", "—", "idle"]
    ]);
    const facts = bannerFacts({ strategies: [], factors: [], freshness: rows, nowMs: NOW });
    expect(facts[2].value).toBe("15 分钟");
  });

  it("counts stale data as something to handle, without repeating what a monitor alert already covers", () => {
    const freshness = freshnessRows(
      {
        pipeline: [
          {
            name: "存储",
            datasets: [
              { producer: "storage", dataset_id: "dataset_abc", freq: "1m", status: "degraded", lag_seconds: "52920" },
              { producer: "storage", dataset_id: "dataset_abc", freq: "1h", status: "degraded", lag_seconds: "18660" }
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
      ["数据", "入库 · 现货K线 · 1m", "落后 14 小时 42 分"],
      ["数据", "入库 · 现货K线 · 1h", "落后 5 小时 11 分"]
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
});
