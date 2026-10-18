import { describe, expect, it } from "vitest";
import { marketClock, marketDay, marketTime, marketZone, zoneLabel } from "./market-time";

const now = Date.parse("2026-10-16T18:56:10Z");

describe("market time", () => {
  it("labels the zone as UTC or UTC+8", () => {
    expect(zoneLabel("UTC")).toBe("UTC");
    expect(zoneLabel(undefined)).toBe("UTC");
    expect(zoneLabel("Asia/Shanghai", now)).toBe("UTC+8");
    expect(marketZone("Not/AZone")).toBe("UTC");
  });

  it("formats in the market zone, with the date only on other days", () => {
    expect(marketTime("2026-10-16T18:54:00Z", "UTC", now)).toBe("18:54 UTC");
    expect(marketTime("2026-10-16T18:54:07Z", "UTC", now, { seconds: true })).toBe("18:54:07 UTC");
    expect(marketTime("2026-10-15T23:59:00Z", "UTC", now)).toBe("10-15 23:59 UTC");
    // 北京时间已经是 10-17：同一时刻按 A 股时区算是第二天。
    expect(marketTime("2026-10-16T18:54:00Z", "Asia/Shanghai", now)).toBe("02:54 UTC+8");
    expect(marketTime("2026-10-16T01:31:00Z", "Asia/Shanghai", now)).toBe("10-16 09:31 UTC+8");
    expect(marketTime(undefined, "UTC", now)).toBe("—");
    expect(marketTime(Date.parse("2026-10-16T18:00:00Z"), "UTC", now)).toBe("18:00 UTC");
  });

  it("splits days in the market zone", () => {
    expect(marketDay(now, "UTC")).toBe("2026-10-16");
    expect(marketDay(now, "Asia/Shanghai")).toBe("2026-10-17");
    expect(marketClock(Date.parse("2026-10-16T01:31:00Z"), "Asia/Shanghai")).toBe("09:31");
  });
});
