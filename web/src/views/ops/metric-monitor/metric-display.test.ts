import { describe, expect, it } from "vitest";
import {
  metricDimensionSummary,
  metricDisplayName,
  metricStatusReason,
  metricValueDisplay,
  parseMetricLabels,
  serviceDisplayName
} from "./metric-display";

describe("metric display helpers", () => {
  it("uses friendly names for known services and operational metrics", () => {
    expect(serviceDisplayName("moox_collector")).toBe("行情采集");
    expect(metricDisplayName("moox_collector_market_fetch_timer_headroom")).toBe("采集定时器剩余容量");
    expect(metricDisplayName("moox_custom_queue_depth")).toBe("custom · queue · depth");
  });

  it("turns label JSON into bounded readable dimensions", () => {
    expect(parseMetricLabels('{"symbol":"BTC-USDT","freq":"1m","space_id":"crypto_market"}')).toEqual([
      { key: "freq", label: "频率", value: "1m" },
      { key: "space_id", label: "空间", value: "crypto_market" },
      { key: "symbol", label: "标的", value: "BTC-USDT" }
    ]);
    expect(metricDimensionSummary('{"a":"1","b":"2","c":"3","d":"4"}', 2).overflow).toBe(2);
    expect(parseMetricLabels("not-json")).toEqual([]);
  });

  it("formats values according to the metric meaning", () => {
    expect(metricValueDisplay("moox_factor_source_ready_lag_seconds", 75)).toBe("1.3分钟");
    expect(metricValueDisplay("moox_host_filesystem_usage_percent", 94.5)).toBe("94.5%");
    expect(metricValueDisplay("moox_collector_market_fetch_timer_headroom", 8)).toBe("8个");
    expect(metricValueDisplay("moox_factor_dataset_output_watermark_timestamp_seconds", undefined)).toBe("暂无数据");
  });

  it("explains stale rows without exposing protocol terminology in the table", () => {
    expect(metricStatusReason({ stale: true })).toBe("陈旧数据");
    expect(metricStatusReason({ stale: false })).toBe("数据正常");
  });
});
