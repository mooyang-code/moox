import { describe, expect, it } from "vitest";
import type { ArrivalHistory, HealthOverview } from "@/api/monitor";
import {
  arrivalChains,
  arrivalChartSpec,
  arrivalModel,
  canShiftArrivalWindow,
  defaultArrivalWindow,
  pipelineDisplayNames,
  shiftArrivalWindow,
  visibleArrivalWindow
} from "./arrival-model";

const names = {
  datasets: { dataset_spot_1m: "现货分钟K线", dataset_factor_spot_1m: "现货分钟因子" },
  views: { view_spot_kline_1m: "现货分钟K线视图" }
};
const now = Date.parse("2026-10-16T19:00:00Z");

const overview: HealthOverview = {
  pipeline: [
    {
      id: "storage",
      datasets: [
        { producer: "storage", space_id: "stockcn", dataset_id: "dataset_stockcn_1m", freq: "1m", timezone: "Asia/Shanghai" },
        { producer: "storage", space_id: "crypto", dataset_id: "dataset_spot_1m", freq: "1m", timezone: "UTC" },
        { producer: "storage", space_id: "mooxsys", dataset_id: "dataset_mooxsys_service_metrics", freq: "30s" }
      ]
    },
    {
      id: "strategy",
      datasets: [
        { producer: "strategy", space_id: "crypto", dataset_id: "strategy_abc", freq: "1m", display_name: "动量轮动（26d51f7e）" }
      ]
    }
  ]
};

const history: ArrivalHistory = {
  timezone: "UTC",
  day: "2026-10-16",
  start_at: "2026-10-16T00:00:00Z",
  end_at: "2026-10-17T00:00:00Z",
  series: [
    {
      stage: "storage",
      dataset_id: "dataset_spot_1m",
      lateness_limit_seconds: "30",
      points: [
        { close_at: "2026-10-16T18:55:00Z", delay_seconds: "1" },
        { close_at: "2026-10-16T18:56:00Z", delay_seconds: "45" },
        { close_at: "2026-10-16T18:59:00Z", delay_seconds: "3", skipped_bars: 2 }
      ]
    },
    {
      stage: "view",
      dataset_id: "view_spot_kline_1m",
      lateness_limit_seconds: "30",
      points: [{ close_at: "2026-10-16T18:55:00Z", delay_seconds: "3" }]
    },
    {
      stage: "strategy",
      dataset_id: "strategy_abc",
      lateness_limit_seconds: "60",
      points: [{ close_at: "2026-10-16T18:55:00Z", delay_seconds: "15" }]
    }
  ]
};

describe("arrival chains", () => {
  it("lists ingested market datasets with their zone and leaves out system data", () => {
    const chains = arrivalChains(overview, names);
    expect(chains.map(chain => [chain.key, chain.label, chain.timezone])).toEqual([
      ["crypto/dataset_spot_1m/1m", "现货分钟K线 · 1m", "UTC"],
      ["stockcn/dataset_stockcn_1m/1m", "dataset_stockcn_1m · 1m", "Asia/Shanghai"]
    ]);
  });
});

describe("arrival model", () => {
  it("names each stage and summarizes delays, late bars and skipped bars", () => {
    const model = arrivalModel(history, names, pipelineDisplayNames(overview));
    expect(model.zone).toBe("UTC");
    expect(model.series.map(series => series.name)).toEqual([
      "入库：现货分钟K线",
      "视图：现货分钟K线视图",
      "策略计算：动量轮动（26d51f7e）"
    ]);
    const storage = model.series[0];
    expect(storage.points.map(point => point.late)).toEqual([false, true, false]);
    expect(storage.lateCount).toBe(1);
    expect(storage.summary).toBe("3 根 · 中位延迟 3 秒 · 最大 45 秒 · 超过允许迟到 1 根 · 补数据跳过 2 根");
    expect(model.notice).toBe("");
  });

  it("explains empty days and days beyond the retention", () => {
    expect(arrivalModel({ ...history, series: [] }, names).notice).toBe("这一天还没有到达记录");
    expect(arrivalModel({ ...history, retention_truncated: true }, names).notice).toContain("7 天的保留期");
    expect(arrivalModel({ ...history, truncated: true }, names).notice).toContain("超过一次查询的上限");
    expect(arrivalModel(undefined, names).notice).toBe("");
  });

  it("builds a time axis in the market zone with the lateness limits marked", () => {
    const model = arrivalModel(
      { ...history, timezone: "Asia/Shanghai", start_at: "2026-10-16T16:00:00Z", end_at: "2026-10-17T16:00:00Z" },
      names
    );
    const spec = arrivalChartSpec(model, now) as any;
    expect(spec.data[0].values).toHaveLength(5);
    const bottom = spec.axes.find((axis: any) => axis.orient === "bottom");
    expect(bottom.min).toBe(Date.parse("2026-10-16T16:00:00Z"));
    expect(bottom.title.text).toBe("收线时间（UTC+8）");
    expect(bottom.label.formatMethod(Date.parse("2026-10-16T01:31:00Z"))).toBe("09:31");
    expect(spec.markLine.map((line: any) => line.y)).toEqual([30, 60]);
    expect(spec.point.style.size({ late: true, skipped: 0 })).toBe(6);
    // 每条线不超过 48 个点：正常的点也画出来，按所在曲线的颜色。
    expect(spec.point.style.size({ late: false, skipped: 0 })).toBe(4);
    expect(spec.point.style.fill({ late: false, skipped: 0, series: model.series[1].name })).toBe("#16a34a");
    expect(spec.point.style.fill({ late: false, skipped: 2, series: model.series[1].name })).toBe("#ff7d00");
    // 分钟线一天上千个点：只画超时和补数据跳过的点。
    const dense = arrivalModel(
      {
        ...history,
        series: [
          {
            stage: "storage",
            dataset_id: "dataset_spot_1m",
            lateness_limit_seconds: "30",
            points: Array.from({ length: 60 }, (_, i) => ({
              close_at: new Date(Date.parse("2026-10-16T00:01:00Z") + i * 60_000).toISOString(),
              delay_seconds: "1"
            }))
          }
        ]
      },
      names
    );
    expect((arrivalChartSpec(dense, now) as any).point.style.size({ late: false, skipped: 0 })).toBe(0);
    expect(spec.tooltip.dimension.content[0].value({ delay: 45, skipped: 2 })).toBe("延迟 45 秒（此前补数据跳过 2 根）");
    expect(spec.tooltip.mark.title.value({ x: Date.parse("2026-10-16T01:31:00Z") })).toBe("10-16 09:31:00 UTC+8 收线");
  });
});

describe("arrival window", () => {
  const model = arrivalModel(history, names);
  const at = (clock: string) => Date.parse(`2026-10-16T${clock}:00Z`);
  const dayEnd = Date.parse("2026-10-17T00:00:00Z");
  const window = (startMs: number, endMs: number) => ({ startMs, endMs });
  const visible = (nowMs: number) => visibleArrivalWindow(model, defaultArrivalWindow(model, nowMs));

  it("shows two hours before and one hour after now, clipped to the day", () => {
    expect(visible(at("18:56"))).toEqual(window(at("16:56"), at("19:56")));
    expect(visible(at("00:30"))).toEqual(window(at("00:00"), at("01:30")));
    expect(visible(at("23:30"))).toEqual(window(at("21:30"), dayEnd));
  });

  it("centers a past day on its worst delay, or its end when it has no records", () => {
    const later = Date.parse("2026-10-18T10:00:00Z");
    // 当天延迟最大的是 18:56 收线那根（45 秒）。
    expect(visibleArrivalWindow(model, defaultArrivalWindow(model, later))).toEqual(window(at("16:56"), at("19:56")));
    const empty = arrivalModel({ ...history, series: [] }, names);
    expect(visibleArrivalWindow(empty, defaultArrivalWindow(empty, later))).toEqual(window(at("22:00"), dayEnd));
  });

  it("moves the anchor an hour at a time, so moving back restores the range", () => {
    const hour = 3_600_000;
    const start = defaultArrivalWindow(model, at("00:30"));
    const later = shiftArrivalWindow(model, start, hour);
    // 截在 0 点的窗口往后挪 1 小时，右边界也只走 1 小时。
    expect(visibleArrivalWindow(model, later)).toEqual(window(at("00:00"), at("02:30")));
    expect(visibleArrivalWindow(model, shiftArrivalWindow(model, later, -hour))).toEqual(window(at("00:00"), at("01:30")));
    // 锚点不出当天：挪到头就停住，按钮随之不可用。
    const first = shiftArrivalWindow(model, start, -5 * hour);
    expect(visibleArrivalWindow(model, first)).toEqual(window(at("00:00"), at("01:00")));
    expect(canShiftArrivalWindow(model, first)).toEqual({ earlier: false, later: true });
    const last = shiftArrivalWindow(model, start, 30 * hour);
    expect(visibleArrivalWindow(model, last)).toEqual(window(at("22:00"), dayEnd));
    expect(canShiftArrivalWindow(model, last)).toEqual({ earlier: true, later: false });
  });

  it("draws only the points inside the window with finer ticks", () => {
    const spec = arrivalChartSpec(model, now, window(at("18:55"), at("18:57"))) as any;
    expect(spec.data[0].values.map((value: any) => value.x)).toEqual([at("18:55"), at("18:56"), at("18:55"), at("18:55")]);
    const bottom = spec.axes.find((axis: any) => axis.orient === "bottom");
    expect([bottom.min, bottom.max]).toEqual([at("18:55"), at("18:57")]);
    expect(bottom.tick.tickStep).toBe(15 * 60 * 1000);
    // 纵轴包住允许迟到（策略 60 秒、其余 30 秒）与可见的最大延迟（45 秒），留一点余量：60 × 1.15 取整为 69。
    expect(spec.axes.find((axis: any) => axis.orient === "left").max).toBe(69);
    expect((arrivalChartSpec(model, now) as any).axes.find((axis: any) => axis.orient === "bottom").tick.tickStep).toBe(
      2 * 3_600_000
    );
    // 配色按全部曲线固定：窗口里没有点的曲线也占着自己的颜色。
    expect(spec.color.domain).toEqual(model.series.map(series => series.name));
  });
});
