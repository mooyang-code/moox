import type { EquityPoint } from "@/api/trade/types";

export interface EquitySeriesPoint {
  time: number;
  value: number;
}

export function toEquitySeries(points: EquityPoint[]): EquitySeriesPoint[] {
  return [...points]
    .sort((a, b) => Number(a.bucket_time) - Number(b.bucket_time))
    .map(point => ({ time: Number(point.bucket_time), value: Number(point.equity) }));
}
