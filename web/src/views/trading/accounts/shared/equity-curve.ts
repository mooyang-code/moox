import type { EquityPoint } from "@/api/trade/types";
import { formatInstant, msToWallClock, zoneSuffix } from "@/utils/time-zone";
import { fmtMoney } from "./format";

export interface EquitySeriesPoint {
  time: number;
  value: number;
}

export function toEquitySeries(points: EquityPoint[]): EquitySeriesPoint[] {
  return [...points]
    .sort((a, b) => Number(a.bucket_time) - Number(b.bucket_time))
    .map(point => ({ time: Number(point.bucket_time), value: Number(point.equity) }));
}

export function equityRange(days: number, now: number) {
  return { start_time: String(now - days * 86_400_000), end_time: String(now) };
}

/**
 * 横轴和提示框：VChart 的时间轴只认本地和 UTC，这里用毫秒的线性轴，刻度和提示框按显示时区格式化，
 * 时区写在轴标题上。
 */
export function equityChartParts(series: EquitySeriesPoint[], zone: string) {
  const first = series[0]?.time ?? Date.now();
  const last = series[series.length - 1]?.time ?? first;
  const title = { value: (datum?: { time?: number }) => formatInstant(datum?.time, zone) };
  const content = [{ key: "权益", value: (datum?: { value?: number }) => fmtMoney(datum?.value) }];
  return {
    axes: [
      {
        orient: "bottom" as const,
        type: "linear" as const,
        zero: false,
        nice: false,
        min: first,
        max: last,
        title: { visible: true, text: `时间（${zoneSuffix(first, zone)}）` },
        label: { formatMethod: (text: string | string[]) => msToWallClock(Number(text), zone, "minute").slice(5) }
      },
      // 纵轴不从 0 起，按数据范围取刻度：权益在高位小幅波动时也看得出变化
      { orient: "left" as const, type: "linear" as const, zero: false }
    ],
    tooltip: { mark: { title, content }, dimension: { title, content } }
  };
}
