import type { ArrivalHistory, HealthOverview } from "@/api/monitor";
import { humanizeDuration, toMillis, toSeconds, type NameBook } from "@/views/home/home-model";
import { marketClock, marketTime, marketZone, zoneLabel } from "./market-time";

/** 一条数据链路：入库环节的源数据集加周期；时间按它的市场时区。 */
export interface ArrivalChain {
  key: string;
  label: string;
  spaceId: string;
  datasetId: string;
  freq: string;
  timezone: string;
}

/** 链路选项：数据链路“入库”环节的每个数据集一条（同一数据集不同周期分开）；没有交易日历的系统数据不记录到达，不列出。 */
export function arrivalChains(overview: HealthOverview | undefined, names: NameBook): ArrivalChain[] {
  const storage = (overview?.pipeline || []).find(stage => stage.id === "storage");
  return (storage?.datasets || [])
    .filter(item => item.space_id && item.space_id !== "mooxsys" && item.dataset_id && item.freq)
    .map(item => {
      const id = item.dataset_id || "";
      const name = item.display_name || names.datasets[id] || id;
      return {
        key: `${item.space_id}/${id}/${item.freq}`,
        label: `${name} · ${item.freq}`,
        spaceId: item.space_id || "",
        datasetId: id,
        freq: item.freq || "",
        timezone: marketZone(item.timezone)
      };
    })
    .sort((a, b) => a.spaceId.localeCompare(b.spaceId) || a.label.localeCompare(b.label, "zh-CN"));
}

const STAGE_NAMES: Record<string, string> = {
  storage: "入库",
  view: "视图",
  factor: "因子",
  factor_view: "因子视图",
  strategy: "策略计算"
};

export interface ArrivalPointModel {
  /** 收线时间（毫秒）。 */
  x: number;
  /** 到达延迟（秒）。 */
  delay: number;
  /** 和上一条记录之间跳过的根数（补数据时一批写完，中间几根不单独记录）。 */
  skipped: number;
  /** 延迟超过允许迟到。 */
  late: boolean;
}

export interface ArrivalSeriesModel {
  key: string;
  /** 例如“视图：现货分钟K线视图”。 */
  name: string;
  stage: string;
  limitSeconds: number;
  points: ArrivalPointModel[];
  /** 汇总：根数、中位延迟、最大延迟、超过允许迟到的根数、跳过的根数。 */
  summary: string;
  lateCount: number;
}

export interface ArrivalModel {
  timezone: string;
  /** 时区标记，例如 UTC、UTC+8。 */
  zone: string;
  day: string;
  startMs: number;
  endMs: number;
  series: ArrivalSeriesModel[];
  /** 没有记录、超出保留期等提示；为空表示正常。 */
  notice: string;
}

function median(values: number[]): number {
  if (!values.length) return 0;
  const sorted = [...values].sort((a, b) => a - b);
  return sorted[Math.floor((sorted.length - 1) / 2)];
}

/** 整理到达历史：每个环节一条曲线，名称按存储元数据或生产者上报的展示名称翻译。 */
export function arrivalModel(
  history: ArrivalHistory | undefined,
  names: NameBook,
  displayNames: Record<string, string> = {}
): ArrivalModel {
  const timezone = marketZone(history?.timezone);
  const startMs = toMillis(history?.start_at) ?? 0;
  const endMs = toMillis(history?.end_at) ?? 0;
  const series = (history?.series || []).map(item => {
    const id = item.dataset_id || "";
    const stage = item.stage || "";
    const limitSeconds = toSeconds(item.lateness_limit_seconds) || 0;
    const points = (item.points || [])
      .map(point => {
        const delay = toSeconds(point.delay_seconds) || 0;
        return {
          x: toMillis(point.close_at) ?? 0,
          delay,
          skipped: Number(point.skipped_bars) || 0,
          late: limitSeconds > 0 && delay > limitSeconds
        };
      })
      .filter(point => point.x > 0);
    const delays = points.map(point => point.delay);
    const lateCount = points.filter(point => point.late).length;
    const skipped = points.reduce((sum, point) => sum + point.skipped, 0);
    const parts = [
      `${points.length} 根`,
      `中位延迟 ${humanizeDuration(median(delays))}`,
      `最大 ${humanizeDuration(delays.length ? Math.max(...delays) : 0)}`
    ];
    if (lateCount) parts.push(`超过允许迟到 ${lateCount} 根`);
    if (skipped) parts.push(`补数据跳过 ${skipped} 根`);
    const name = displayNames[id] || names.datasets[id] || names.views[id] || id;
    return {
      key: `${stage}/${id}`,
      name: `${STAGE_NAMES[stage] || stage}：${name}`,
      stage,
      limitSeconds,
      points,
      summary: parts.join(" · "),
      lateCount
    };
  });
  let notice = "";
  if (history?.retention_truncated) notice = "这一天早于 7 天的保留期，曲线可能不完整";
  else if (history?.truncated) notice = "记录超过一次查询的上限，曲线只含这一天前面的部分";
  else if (history && !series.length) notice = "这一天还没有到达记录";
  return { timezone, zone: zoneLabel(timezone, startMs || Date.now()), day: history?.day || "", startMs, endMs, series, notice };
}

const SERIES_COLORS = ["#2563eb", "#16a34a", "#7c3aed", "#0891b2", "#d97706", "#db2777"];

const HOUR_MS = 60 * 60 * 1000;
/** 默认窗口：锚点之前 2 小时、之后 1 小时；全天画在一起分钟线会挤成一团。 */
export const WINDOW_BEFORE_MS = 2 * HOUR_MS;
export const WINDOW_AFTER_MS = HOUR_MS;
/** 前后挪动窗口的步长。 */
export const WINDOW_STEP_MS = HOUR_MS;

/** 时间范围（毫秒，左闭右闭）。 */
export interface ArrivalWindow {
  startMs: number;
  endMs: number;
}

/**
 * 默认窗口：看今天时以当前时间为锚点；看历史日期时以当天延迟最大的那一根为锚点（没有记录时取当天结束），
 * 取锚点前 2 小时到后 1 小时。返回的窗口不截断，显示时再用 visibleArrivalWindow 截到当天，
 * 这样前后挪动始终是锚点移 1 小时，来回挪能回到原处。
 */
export function defaultArrivalWindow(model: ArrivalModel, nowMs: number): ArrivalWindow {
  let anchor = model.endMs;
  if (nowMs >= model.startMs && nowMs < model.endMs) {
    anchor = nowMs;
  } else {
    let worst = -1;
    for (const series of model.series) {
      for (const point of series.points) {
        if (point.delay > worst) {
          worst = point.delay;
          anchor = point.x;
        }
      }
    }
  }
  return { startMs: anchor - WINDOW_BEFORE_MS, endMs: anchor + WINDOW_AFTER_MS };
}

/** 窗口的锚点。 */
function anchorOf(window: ArrivalWindow): number {
  return window.startMs + WINDOW_BEFORE_MS;
}

/** 把锚点挪动 deltaMs（锚点不出当天），窗口仍是锚点前 2 小时到后 1 小时。 */
export function shiftArrivalWindow(model: ArrivalModel, window: ArrivalWindow, deltaMs: number): ArrivalWindow {
  const anchor = Math.min(Math.max(anchorOf(window) + deltaMs, model.startMs), model.endMs);
  return { startMs: anchor - WINDOW_BEFORE_MS, endMs: anchor + WINDOW_AFTER_MS };
}

/** 锚点还能往早、往晚挪。 */
export function canShiftArrivalWindow(model: ArrivalModel, window: ArrivalWindow): { earlier: boolean; later: boolean } {
  const anchor = anchorOf(window);
  return { earlier: anchor > model.startMs, later: anchor < model.endMs };
}

/** 显示用的范围：跨天的部分以当天的界限为准截掉。 */
export function visibleArrivalWindow(model: ArrivalModel, window: ArrivalWindow): ArrivalWindow {
  return { startMs: Math.max(window.startMs, model.startMs), endMs: Math.min(window.endMs, model.endMs) };
}

/** 窗口跨度对应的刻度间隔：3 小时 15 分钟一格，6 小时以内 30 分钟，全天 2 小时（UTC 与 UTC+8 的整点都落在倍数上）。 */
function tickStepFor(spanMs: number): number {
  if (spanMs <= 3 * HOUR_MS) return 15 * 60 * 1000;
  if (spanMs <= 6 * HOUR_MS) return 30 * 60 * 1000;
  return 2 * HOUR_MS;
}

/**
 * VChart 折线图配置：横轴是收线时间（按市场时区标注），纵轴是到达延迟（秒）；超过允许迟到的点标红，补数据跳过处标橙。
 * 只画窗口内的点（不传窗口为全天），纵轴按可见的点缩放。
 */
export function arrivalChartSpec(model: ArrivalModel, nowMs: number, window?: ArrivalWindow): Record<string, unknown> {
  const range = window || { startMs: model.startMs, endMs: model.endMs };
  const visible = model.series.map(series => ({
    name: series.name,
    points: series.points.filter(point => point.x >= range.startMs && point.x <= range.endMs)
  }));
  const values = visible.flatMap(series => series.points.map(point => ({ ...point, series: series.name })));
  const limits = [...new Set(model.series.map(series => series.limitSeconds).filter(limit => limit > 0))];
  // 点稀疏时（小时线一天 24 根，日线、周线、月线一天至多一根）正常的点也画出来，否则单点连不成线、什么都看不到。
  const sparse = visible.every(series => series.points.length <= 24);
  // 纵轴包住允许迟到：延迟都很小时也看得到虚线，知道离允许迟到还有多远。
  const yMax = Math.ceil(Math.max(1, ...limits, ...values.map(value => value.delay)) * 1.15);
  const colors = new Map(model.series.map((series, index) => [series.name, SERIES_COLORS[index % SERIES_COLORS.length]]));
  const closeTitle = (datum: ArrivalPointModel) => `${marketTime(datum.x, model.timezone, nowMs, { seconds: true })} 收线`;
  const delayLine = {
    key: (datum: ArrivalPointModel & { series: string }) => datum.series,
    value: (datum: ArrivalPointModel) =>
      `延迟 ${humanizeDuration(datum.delay)}${datum.skipped ? `（此前补数据跳过 ${datum.skipped} 根）` : ""}`
  };
  return {
    type: "line",
    data: [{ id: "arrivals", values }],
    xField: "x",
    yField: "delay",
    seriesField: "series",
    // 按全部曲线固定配色：窗口里没有点的曲线也占着自己的颜色，线和点的颜色不会错位。
    color: {
      type: "ordinal",
      domain: model.series.map(series => series.name),
      range: model.series.map(series => colors.get(series.name))
    },
    line: { style: { lineWidth: 1.5, curveType: "linear" } },
    point: {
      style: {
        size: (datum: ArrivalPointModel) => (datum.late || datum.skipped ? 6 : sparse ? 4 : 0),
        fill: (datum: ArrivalPointModel & { series: string }) =>
          datum.late ? "#f53f3f" : datum.skipped ? "#ff7d00" : colors.get(datum.series)
      }
    },
    legends: { visible: true, orient: "top" },
    axes: [
      { orient: "left", type: "linear", min: 0, max: yMax, title: { visible: true, text: "到达延迟（秒）" } },
      {
        orient: "bottom",
        type: "linear",
        min: range.startMs,
        max: range.endMs,
        nice: false,
        tick: { tickStep: tickStepFor(range.endMs - range.startMs) },
        title: { visible: true, text: `收线时间（${model.zone}）` },
        label: { formatMethod: (value: number) => marketClock(value, model.timezone) }
      }
    ],
    markLine: limits.map(limit => ({
      y: limit,
      line: { style: { stroke: "#f53f3f", lineDash: [4, 4], lineWidth: 1 } },
      label: { visible: true, text: `允许迟到 ${humanizeDuration(limit)}`, position: "insideEndTop" }
    })),
    tooltip: {
      dimension: { title: { value: closeTitle }, content: [delayLine] },
      mark: { title: { value: closeTitle }, content: [delayLine] }
    },
    crosshair: { xField: { visible: true } }
  };
}

/** 生产者上报的展示名称（例如策略实例名），按数据集 ID 索引。 */
export function pipelineDisplayNames(overview: HealthOverview | undefined): Record<string, string> {
  const out: Record<string, string> = {};
  for (const stage of overview?.pipeline || []) {
    for (const item of stage.datasets || []) {
      if (item.dataset_id && item.display_name) out[item.dataset_id] = item.display_name;
    }
  }
  return out;
}
