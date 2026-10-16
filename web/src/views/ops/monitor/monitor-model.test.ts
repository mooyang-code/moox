import { describe, expect, it } from "vitest";
import type { HealthOverview } from "@/api/monitor";
import {
  alertGroups,
  businessKinds,
  capacityText,
  componentsByHost,
  hostCards,
  incidentTimeline,
  layerCountText,
  layerSummaries,
  pipelineStages,
  verdict
} from "./monitor-model";

const names = { datasets: { dataset_abc: "现货分钟K线" }, views: {} };
const now = Date.parse("2026-10-16T03:00:00Z");
const at = (minutesAgo: number, seconds = 0) => new Date(now - minutesAgo * 60_000 + seconds * 1000).toISOString();

describe("current alerts", () => {
  const overview: HealthOverview = {
    generated_at: new Date(now).toISOString(),
    alerts: [
      {
        id: "fetch",
        severity: "critical",
        title: "行情采集 scf_timer 协调",
        reason: "采集计划指标已停止上报",
        triggered_at: at(2, 20),
        object: { type: "business", kind: "market_fetch" }
      },
      {
        id: "reporter",
        severity: "warning",
        title: "行情采集服务（control 节点）· 运行指标上报",
        triggered_at: at(2),
        object: { type: "component", host_id: "control", component_id: "collector" }
      },
      {
        id: "dataset",
        severity: "critical",
        title: "dataset_abc · 1m",
        reason: "任务运行结果已过期",
        triggered_at: at(189),
        recovering: true,
        object: { type: "dataset", producer: "factor" },
        dataset: {
          status: "stale",
          output_watermark_at: at(60),
          latest_bar_close_at: at(59),
          waiting_bars: 58,
          waiting_since: at(58),
          lateness_limit_seconds: "180"
        }
      }
    ]
  };

  it("groups alerts triggered within a minute and puts the longest first", () => {
    const groups = alertGroups(overview, names, now);
    expect(groups.map(group => group.lines.map(line => line.key))).toEqual([["dataset"], ["fetch", "reporter"]]);
    expect(groups[0].duration).toBe("3 小时 9 分");
    expect(groups[1].tone).toBe("bad");
    expect(groups[1].lines[1].tone).toBe("warn");
    expect(groups[1].lines[0].link.label).toBe("查看采集结果");
  });

  it("translates dataset ids and exposes the dataset facts", () => {
    const [line] = alertGroups(overview, names, now)[0].lines;
    expect(line.title).toBe("现货分钟K线 · 1m");
    expect(line.recovering).toBe(true);
    expect(line.facts).toEqual(
      expect.arrayContaining([
        { label: "到达", value: "等待中，已过收线 58 分钟（缺 58 根）" },
        { label: "允许迟到", value: "3 分钟" }
      ])
    );
    expect(line.link.href).toBe("#/factor/tasks?tab=results");
  });

  it("summarizes the verdict from alerts first", () => {
    const groups = alertGroups(overview, names, now);
    const result = verdict({ overview, loaded: true, error: "", groups, layers: layerSummaries(overview), nowMs: now });
    expect(result).toEqual({
      tone: "bad",
      title: "3 项告警进行中",
      detail: "最长已持续 3 小时 9 分 · 其中 2 项同一时间触发，可能是同一个原因"
    });
  });
});

describe("verdict without alerts", () => {
  const healthy: HealthOverview = {
    generated_at: new Date(now).toISOString(),
    topology_known: true,
    components: [
      { host_id: "control", component_id: "admin", status: "healthy" },
      { host_id: "control", component_id: "host-agent", status: "unchecked", reporter: { status: "unchecked" } }
    ],
    hosts: [{ host_id: "control", status: "healthy" }]
  };
  const run = (overview: HealthOverview, error = "") =>
    verdict({ overview, loaded: true, error, groups: [], layers: layerSummaries(overview), nowMs: now });

  it("is green only with a known topology and nothing to watch", () => {
    expect(run(healthy)).toEqual({ tone: "ok", title: "全部正常", detail: "1 个组件 · 1 台主机" });
    expect(run({ ...healthy, topology_known: false }).title).toBe("部署清单尚未同步");
  });

  it("counts degraded and unknown items as needing attention", () => {
    const overview = { ...healthy, pipeline: [{ id: "factor", datasets: [{ status: "degraded" }, { status: "unknown" }] }] };
    expect(run(overview)).toEqual({ tone: "warn", title: "没有告警，2 项需要关注", detail: "数据链路 2" });
  });

  it("never shows green after a failed refresh", () => {
    const result = run(healthy, "网络错误");
    expect(result.tone).toBe("idle");
    expect(result.title).toBe("监控数据刷新失败");
    expect(result.detail).toContain("不代表当前状态");
  });

  it("keeps not-probed components out of the healthy ratio", () => {
    const [components] = layerSummaries(healthy);
    expect(layerCountText(components)).toBe("1 / 1 正常 · 1 项停用或不探测");
  });
});

describe("incident timeline", () => {
  const overview: HealthOverview = {
    generated_at: new Date(now).toISOString(),
    incident_window_hours: 48,
    incidents: [
      { id: "a", severity: "warning", title: "行情采集服务 · 运行指标上报", triggered_at: at(30), resolved_at: at(30, 29) },
      {
        id: "b",
        severity: "critical",
        title: "行情采集 scf_timer 协调",
        triggered_at: at(30, 1),
        resolved_at: at(30, 29),
        notify_failed: true
      },
      {
        id: "c",
        severity: "critical",
        title: "策略结果",
        reason: "一直运行失败",
        triggered_at: at(600),
        resolved_at: at(411),
        reminder_count: 37
      },
      { id: "d", severity: "critical", title: "控制台前端 · 健康检查", triggered_at: at(5), active: true },
      { id: "e", severity: "critical", title: "采集计划", triggered_at: at(120) }
    ]
  };

  it("merges simultaneous incidents, marks flaps and keeps the newest first", () => {
    const timeline = incidentTimeline(overview, names, now);
    expect(timeline.total).toBe(5);
    expect(timeline.rows.map(row => row.key)).toEqual(["d", "a|b", "e", "c"]);
    const [active, flap, revoked, long] = timeline.rows;
    expect(active).toMatchObject({ active: true, tone: "bad", outcome: "进行中 · 已持续 5 分钟" });
    expect(flap).toMatchObject({
      title: "行情采集 scf_timer 协调",
      others: ["行情采集服务 · 运行指标上报"],
      flap: true,
      notifyFailed: true
    });
    expect(flap.outcome).toBe("29 秒后恢复");
    expect(flap.time).toMatch(/^\d{2}:\d{2}$/);
    expect(revoked.outcome).toBe("已撤销（检查停用或移除）");
    expect(long).toMatchObject({ flap: false, reminders: 37, outcome: "3 小时 9 分后恢复" });
    expect(timeline.flaps).toBe(1);
    expect(timeline.active).toBe(1);
  });

  it("never merges incidents in different lifecycle states", () => {
    const timeline = incidentTimeline(
      {
        generated_at: new Date(now).toISOString(),
        incidents: [
          { id: "resolved", severity: "critical", title: "已恢复项", triggered_at: at(30), resolved_at: at(25) },
          { id: "revoked", severity: "critical", title: "已撤销项", triggered_at: at(30, 10) },
          { id: "ongoing", severity: "warning", title: "进行中项", triggered_at: at(30, 20), resolved_at: at(29), active: true }
        ]
      },
      names,
      now
    );
    expect(timeline.rows.map(row => [row.key, row.outcome])).toEqual([
      ["ongoing", "进行中 · 已持续 29 分钟"],
      ["revoked", "已撤销（检查停用或移除）"],
      ["resolved", "5 分钟后恢复"]
    ]);
  });
});

describe("layers", () => {
  it("groups components by host with problems first", () => {
    const hosts = componentsByHost({
      components: [
        { host_id: "storage", component_id: "storage-node", name: "存储数据节点", status: "healthy" },
        { host_id: "control", component_id: "admin", name: "管理后台", status: "healthy" },
        { host_id: "control", component_id: "collector", name: "行情采集服务", status: "down", reason: "健康检查失败" },
        { host_id: "control", component_id: "eventbus", name: "消息总线", status: "unchecked", reporter: { status: "degraded" } }
      ]
    });
    expect(hosts.map(host => host.hostId)).toEqual(["control", "storage"]);
    expect(hosts[0].chips.map(chip => [chip.name, chip.tone, chip.status])).toEqual([
      ["行情采集服务", "bad", "异常"],
      ["消息总线", "warn", "不探测 · 上报需关注"],
      ["管理后台", "ok", "正常"]
    ]);
  });

  it("shows each dataset's latest bar delay: on time, late but arrived, waiting, and calendar issues", () => {
    const close = at(10);
    const stages = pipelineStages(
      {
        pipeline: [
          { id: "collection", status: "healthy", datasets: [] },
          {
            id: "factor",
            status: "healthy",
            datasets: [
              {
                producer: "factor",
                space_id: "crypto",
                dataset_id: "dataset_abc",
                freq: "1m",
                status: "healthy",
                output_watermark_at: at(11),
                latest_bar_close_at: close,
                arrived_at: at(10, 5),
                delay_known: true,
                delay_seconds: "5",
                lateness_limit_seconds: "600"
              },
              {
                producer: "factor",
                space_id: "crypto",
                dataset_id: "dataset_late",
                freq: "1m",
                status: "healthy",
                output_watermark_at: at(41),
                latest_bar_close_at: at(40),
                arrived_at: at(0),
                delay_known: true,
                delay_seconds: "2400",
                lateness_limit_seconds: "600"
              },
              {
                producer: "factor",
                space_id: "crypto",
                dataset_id: "dataset_wait",
                freq: "1m",
                status: "stale",
                reason: "等待中",
                output_watermark_at: at(15),
                waiting_bars: 3,
                waiting_since: at(12),
                overdue_seconds: "700",
                lateness_limit_seconds: "600"
              },
              {
                producer: "factor",
                space_id: "stockcn",
                dataset_id: "dataset_cal",
                freq: "1m",
                status: "unknown",
                reason: "日历未覆盖：2027-01-04",
                clock_issue: "日历未覆盖：2027-01-04",
                lateness_limit_seconds: "600"
              }
            ]
          },
          { id: "trade", status: "disabled", datasets: [] }
        ]
      },
      names,
      now
    );
    expect(stages.map(stage => [stage.label, stage.total, stage.note])).toEqual([
      ["采集", 0, "服务正常，暂无数据集观测"],
      ["因子", 4, ""],
      ["交易", 0, "未部署"]
    ]);
    expect(stages[1].rows.map(row => [row.name, row.delay, row.delayTone, row.attention])).toEqual([
      // 还没到：从收线起按页面时钟计时，超过允许迟到为红。
      ["dataset_wait", "等待中，已过收线 12 分钟（缺 3 根）", "bad", true],
      ["dataset_cal", "日历未覆盖：2027-01-04", "bad", true],
      // 迟到但已到：标橙，当前状态仍正常。
      ["dataset_late", "延迟 40 分钟", "warn", false],
      // 准时：延迟在到达那一刻定值。
      ["现货分钟K线", "延迟 5 秒", "ok", false]
    ]);
    expect(stages[1].rows[2].delayTitle).toContain("迟到，已恢复");
    expect(stages[1].rows[3].limit).toBe("10 分钟");
  });

  it("labels business checks by kind and treats retired checks as inactive", () => {
    const kinds = businessKinds(
      {
        business_checks: [
          {
            check_id: "kline_freshness:crypto:view_a:1m",
            name: "现货分钟K线 · K线结果",
            kind: "kline_freshness",
            status: "healthy"
          },
          {
            check_id: "kline_freshness:stockcn:view_b:1m",
            kind: "kline_freshness",
            status: "disabled",
            reason: "已停用或移除，不再检查"
          },
          {
            check_id: "market_fetch:scf_timer",
            kind: "market_fetch",
            module: "scf_timer",
            status: "down",
            reason: "采集计划指标已停止上报"
          }
        ]
      },
      names,
      now
    );
    expect(kinds.map(kind => [kind.label, kind.healthy, kind.total, kind.inactive, kind.tone])).toEqual([
      ["K 线新鲜度", 1, 2, 1, "ok"],
      ["采集计划对账", 0, 1, 0, "bad"]
    ]);
    expect(kinds[1].items[0]).toMatchObject({ title: "采集计划对账 · scf_timer", attention: true });
  });

  it("draws host meters against their alert thresholds", () => {
    const [card] = hostCards(
      {
        hosts: [
          {
            host_id: "control",
            agent_id: "pwU2",
            address: "106.53.107.122",
            status: "healthy",
            cpu_available: true,
            cpu_percent: 85,
            cpu_threshold: 80,
            memory_available: true,
            memory_percent: 82,
            memory_threshold: 90,
            disk_available: false
          }
        ]
      },
      now
    );
    expect(card.meters.map(meter => [meter.label, meter.tone, meter.threshold])).toEqual([
      ["CPU", "bad", 80],
      ["内存", "warn", 90],
      ["磁盘", "idle", null]
    ]);
    expect(card.href).toBe("#/ops/hosts?tab=monitor&agent_id=pwU2&host_id=control");
  });

  it("shows host capacity next to each usage meter", () => {
    const [card] = hostCards(
      {
        hosts: [
          {
            host_id: "storage",
            status: "healthy",
            cpu_available: true,
            cpu_percent: 9,
            cpu_cores: 4,
            memory_available: true,
            memory_percent: 40,
            memory_total_bytes: String(8 * 1024 ** 3),
            memory_used_bytes: String(3.2 * 1024 ** 3),
            disk_available: true,
            disk_percent: 62,
            disk_total_bytes: 200 * 1024 ** 3,
            disk_used_bytes: 124 * 1024 ** 3,
            disk_mountpoint: "/data"
          }
        ]
      },
      now
    );
    expect(card.meters.map(meter => meter.capacity)).toEqual(["4 核", "3.2 / 8 GB", "124 / 200 GB"]);
    expect(card.meters[2].capacityTitle).toContain("/data");
    expect(capacityText(undefined, undefined)).toBe("");
    expect(capacityText(512 * 1024 ** 2, 2 * 1024 ** 4)).toBe("0 / 2 TB");
  });

  it("puts strategy results after factors and names rows by the producer's display name", () => {
    const stages = pipelineStages(
      {
        pipeline: [
          { id: "factor", status: "healthy", datasets: [] },
          {
            id: "strategy",
            status: "healthy",
            datasets: [
              {
                producer: "strategy",
                space_id: "crypto",
                dataset_id: "strategy_26d51f7e-4c40-4e33-b637-4ec72d4cd7e6",
                display_name: "binance_spot_momentum（26d51f7e）",
                freq: "1m",
                status: "healthy"
              }
            ]
          },
          { id: "trade", status: "healthy", datasets: [] }
        ]
      },
      names
    );
    expect(stages.map(stage => stage.label)).toEqual(["因子", "策略计算", "交易"]);
    expect(stages[1].rows[0]).toMatchObject({ name: "binance_spot_momentum（26d51f7e）", producer: "策略" });
  });
});
