import { describe, expect, it } from "vitest";
import {
  aggregateNetworkRate,
  historyHasRenderableData,
  maxAvailableFilesystemUsage,
  memoryUsageAvailable,
  metricValueAvailable
} from "./host-monitor-mapping";

describe("host monitor metric mapping", () => {
  it("keeps 0% filesystem and memory usage available when total is valid", () => {
    expect(maxAvailableFilesystemUsage([{ percent: 0, percent_available: true }])).toBe(0);
    expect(memoryUsageAvailable(1024, true)).toBe(true);
    expect(memoryUsageAvailable(0, true)).toBe(false);
    expect(memoryUsageAvailable(1024, false)).toBe(false);
  });

  it("aggregates network rates across available interfaces only", () => {
    expect(
      aggregateNetworkRate([
        { rx_speed: 10, tx_speed: 5, rate_available: true },
        { rx_speed: 7, tx_speed: 3, rate_available: true },
        { rx_speed: 99, tx_speed: 99, rate_available: false }
      ])
    ).toEqual({ rx: 17, tx: 8 });
    expect(aggregateNetworkRate([{ rx_speed: 1, tx_speed: 2, rate_available: false }])).toBeNull();
  });

  it("hides stale current metrics for offline hosts and detects renderable history points", () => {
    expect(metricValueAvailable("offline", true)).toBe(false);
    expect(metricValueAvailable("online", true)).toBe(true);
    expect(historyHasRenderableData([{ cpu_available: false, memory_available: false, disk_available: false }])).toBe(false);
    expect(historyHasRenderableData([{ cpu_available: false, memory_available: true, disk_available: false }])).toBe(true);
  });
});
