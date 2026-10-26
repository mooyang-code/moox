import { describe, expect, it } from "vitest";
import type { TaskRunSummary } from "@/api/factor/types";
import { barLagText, closedBarLag, formatLag, freqSeconds, runBarLag, setHealth } from "./health";

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

  it("treats an up-to-date task as healthy even though its last period opened over a period ago", () => {
    // 开盘时间 LAST 的 1m K 线在 LAST+60 收盘；收盘后 5 秒、65 秒、100 秒时仍是同一期，都是“已追平”。
    for (const lag of [65, 125, 150]) expect(setHealth(enabled, run({ lag_seconds: lag })).key).toBe("ok");
  });

  it("flags a task only after a whole closed bar has been missed", () => {
    expect(setHealth(enabled, run({ lag_seconds: 180 })).key).toBe("ok"); // 落后 1 根：刚收盘还在计算
    expect(setHealth(enabled, run({ lag_seconds: 209 })).key).toBe("ok");
    expect(setHealth(enabled, run({ lag_seconds: 210 })).key).toBe("lagging"); // 落后 2 根
    expect(setHealth({ status: "enabled", freq: "1h" }, run({ lag_seconds: 3600 * 2 })).key).toBe("ok");
  });
});

describe("closedBarLag", () => {
  it("measures against the latest closed bar, not the wall clock", () => {
    expect(closedBarLag("1m", LAST, LAST + 59)).toEqual({ bars: 0, seconds: 0 });
    expect(closedBarLag("1m", LAST, LAST + 125)).toEqual({ bars: 0, seconds: 0 });
    expect(closedBarLag("1m", LAST, LAST + 150)).toEqual({ bars: 1, seconds: 60 });
    expect(closedBarLag("1m", LAST, LAST + 215)).toEqual({ bars: 2, seconds: 120 });
  });

  it("gives a freshly closed bar a grace period before counting it as missed", () => {
    // LAST+60 开盘的那根在 LAST+120 收盘；收盘后 29 秒内仍算追平，30 秒后才算落后 1 根。
    expect(closedBarLag("1m", LAST, LAST + 120 + 29).bars).toBe(0);
    expect(closedBarLag("1m", LAST, LAST + 120 + 30).bars).toBe(1);
  });

  it("never goes negative and tolerates unknown frequencies", () => {
    expect(closedBarLag("1m", LAST + 600, LAST).bars).toBe(0);
    expect(closedBarLag("weird", LAST, LAST + 9999)).toEqual({ bars: 0, seconds: 0 });
    // 105 点 10 秒时，最近已收盘的是 104 点开盘那根；产出停在 100 点，落后 101、102、103 三根。
    expect(closedBarLag("1h", 3600 * 100, 3600 * 105 + 10).bars).toBe(3);
  });

  it("derives the engine summary's own clock from the period and its lag", () => {
    expect(runBarLag("1m", run({ lag_seconds: 8 })).bars).toBe(0);
    expect(runBarLag("1m", run({ lag_seconds: 400 })).bars).toBe(5);
    expect(runBarLag("1m", undefined)).toEqual({ bars: 0, seconds: 0 });
  });

  it("words the result for people", () => {
    expect(barLagText("1m", undefined)).toBe("");
    expect(barLagText("1m", run({ last_period_time: 0 }))).toBe("");
    expect(barLagText("1m", run({ lag_seconds: 70 }))).toBe("已追平");
    expect(barLagText("1m", run({ lag_seconds: 400 }))).toBe("落后 5 根（5 分钟）");
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
