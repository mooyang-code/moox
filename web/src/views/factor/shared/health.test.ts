import { describe, expect, it } from "vitest";
import type { SetRunSummary } from "@/api/factor/types";
import { formatLag, freqSeconds, setHealth } from "./health";

const run = (patch: Partial<SetRunSummary> = {}): SetRunSummary => ({
  set_id: "s",
  last_period_time: 1_700_000_000,
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
    expect(setHealth({ status: "deleting", freq: "1m" }, run()).key).toBe("deleting");
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

  it("flags lag beyond two periods only", () => {
    expect(setHealth(enabled, run({ lag_seconds: 120 })).key).toBe("ok");
    expect(setHealth(enabled, run({ lag_seconds: 121 })).key).toBe("lagging");
    expect(setHealth({ status: "enabled", freq: "1h" }, run({ lag_seconds: 3600 })).key).toBe("ok");
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
