import { describe, expect, it } from "vitest";
import type { TaskRunSummary } from "@/api/factor/types";
import { barLagText, formatLag, freqSeconds, runBarLag, setHealth } from "./health";

const LAST = 1_699_999_980;

const run = (patch: Partial<TaskRunSummary> = {}): TaskRunSummary => ({
  task_id: "s",
  // 周期时间都按周期对齐（这里是整分钟）：服务端写入的就是对齐后的开盘时间。
  last_period_time: LAST,
  last_status: "complete",
  lag_seconds: 30,
  ...patch
});

describe("freqSeconds", () => {
  it("parses minute, hour and day frequencies", () => {
    expect(freqSeconds("1m")).toBe(60);
    expect(freqSeconds("4h")).toBe(14400);
    expect(freqSeconds("1d")).toBe(86400);
    expect(freqSeconds("weird")).toBe(0);
  });
});

describe("setHealth", () => {
  const enabled = { status: "enabled" as const, freq: "1m" };

  it("reports lifecycle states before run state", () => {
    expect(setHealth({ status: "pending", freq: "1m" }, run()).key).toBe("pending");
    expect(setHealth({ status: "disabled", freq: "1m" }, run()).key).toBe("disabled");
  });

  it("is idle without any completed period", () => {
    expect(setHealth(enabled).key).toBe("idle");
    expect(setHealth(enabled, run({ last_period_time: 0 })).key).toBe("idle");
  });

  it("flags failed and degraded periods", () => {
    expect(setHealth(enabled, run({ last_status: "failed" })).key).toBe("failed");
    expect(setHealth(enabled, run({ last_status: "degraded" })).key).toBe("degraded");
  });

  it("reads the engine's behind-bar count: one bar (just closed, computing) is still healthy", () => {
    // lag_seconds 是引擎按市场时钟算好的“落后的已收盘根数 × 周期”。
    expect(setHealth(enabled, run({ lag_seconds: 0 })).key).toBe("ok");
    expect(setHealth(enabled, run({ lag_seconds: 60 })).key).toBe("ok"); // 落后 1 根：刚收盘还在计算
    expect(setHealth(enabled, run({ lag_seconds: 120 })).key).toBe("lagging"); // 落后 2 根
    expect(setHealth({ status: "enabled", freq: "1h" }, run({ lag_seconds: 3600 })).key).toBe("ok");
    expect(setHealth({ status: "enabled", freq: "5m" }, run({ lag_seconds: 600 })).key).toBe("lagging");
  });
});

describe("runBarLag", () => {
  it("converts the engine lag into bars without using the wall clock", () => {
    expect(runBarLag("1m", run({ lag_seconds: 0 }))).toEqual({ bars: 0, seconds: 0 });
    expect(runBarLag("1m", run({ lag_seconds: 300 }))).toEqual({ bars: 5, seconds: 300 });
    // A 股 5m 午休、周末时引擎报 0：不按 7×24 小时推算成落后。
    expect(runBarLag("5m", run({ lag_seconds: 0 })).bars).toBe(0);
    expect(runBarLag("5m", run({ lag_seconds: 900 })).bars).toBe(3);
  });

  it("never goes negative and tolerates unknown frequencies or missing runs", () => {
    expect(runBarLag("1m", run({ lag_seconds: -60 })).bars).toBe(0);
    expect(runBarLag("weird", run({ lag_seconds: 9999 }))).toEqual({ bars: 0, seconds: 0 });
    expect(runBarLag("1m", undefined)).toEqual({ bars: 0, seconds: 0 });
  });

  it("words the result for people", () => {
    expect(barLagText("1m", undefined)).toBe("");
    expect(barLagText("1m", run({ last_period_time: 0 }))).toBe("");
    expect(barLagText("1m", run({ lag_seconds: 0 }))).toBe("已追平");
    expect(barLagText("1m", run({ lag_seconds: 300 }))).toBe("落后 5 根（5 分钟）");
  });
});

describe("formatLag", () => {
  it("scales units and tolerates missing values", () => {
    expect(formatLag(undefined)).toBe("-");
    expect(formatLag(-5)).toBe("0 秒");
    expect(formatLag(90)).toBe("1 分钟");
    expect(formatLag(7200)).toBe("2.0 小时");
  });
});
