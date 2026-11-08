import { describe, expect, it } from "vitest";
import {
  formatInstant,
  marketClock,
  marketDay,
  marketTime,
  msToWallClock,
  resolveZone,
  toInstantMs,
  wallClockToMs,
  zoneSuffix
} from "./time-zone";

const NY = "America/New_York";
const SH = "Asia/Shanghai";

describe("测试环境的时区", () => {
  it("在三个目标时区之外的时区下跑，否则按 UTC 和按本地格式化的结果分不出来", () => {
    expect(new Date(Date.UTC(2026, 0, 1)).getTimezoneOffset()).not.toBe(0);
    expect(process.env.TZ).toBe("America/Los_Angeles");
  });
});

describe("时区后缀", () => {
  it("零偏移写 UTC，东八区写 UTC+8，负号是 U+2212", () => {
    const at = Date.parse("2026-11-07T00:00:00Z");
    expect(zoneSuffix(at, "UTC")).toBe("UTC");
    expect(zoneSuffix(at, SH)).toBe("UTC+8");
    expect(zoneSuffix(at, NY)).toBe("UTC−5");
    expect(zoneSuffix(at, "Asia/Kolkata")).toBe("UTC+5:30");
  });

  it("空或非法的时区按 UTC", () => {
    expect(resolveZone("")).toBe("UTC");
    expect(resolveZone(undefined)).toBe("UTC");
    expect(resolveZone("Not/AZone")).toBe("UTC");
    expect(zoneSuffix(Date.now(), "Not/AZone")).toBe("UTC");
  });

  it("美东夏令时切换前后各一个时刻", () => {
    expect(formatInstant("2026-03-08T06:59:59Z", NY, "clock")).toBe("01:59:59 UTC−5");
    expect(formatInstant("2026-03-08T07:00:00Z", NY, "clock")).toBe("03:00:00 UTC−4");
    expect(formatInstant("2026-11-01T05:59:59Z", NY, "clock")).toBe("01:59:59 UTC−4");
    expect(formatInstant("2026-11-01T06:00:00Z", NY, "clock")).toBe("01:00:00 UTC−5");
  });
});

describe("显示格式", () => {
  const at = "2026-11-07T01:30:05.123456789Z";

  it("到秒、到分钟、只有日期，都带后缀，不显示纳秒", () => {
    expect(formatInstant(at, "UTC")).toBe("2026-11-07 01:30:05 UTC");
    expect(formatInstant(at, SH)).toBe("2026-11-07 09:30:05 UTC+8");
    expect(formatInstant(at, SH, "minute")).toBe("2026-11-07 09:30 UTC+8");
    expect(formatInstant(at, NY, "date")).toBe("2026-11-06 UTC−5");
  });

  it("没有时间时为 —", () => {
    expect(formatInstant(undefined, "UTC")).toBe("—");
    expect(formatInstant("", "UTC")).toBe("—");
    expect(formatInstant("不是时间", "UTC")).toBe("—");
  });
});

describe("解析后端给的时间", () => {
  it("RFC3339、数字偏移、毫秒数字和纯数字字符串", () => {
    const ms = Date.parse("2026-11-07T01:30:05Z");
    expect(toInstantMs("2026-11-07T01:30:05Z")).toBe(ms);
    expect(toInstantMs("2026-11-07T09:30:05+08:00")).toBe(ms);
    expect(toInstantMs("2026-11-07T01:30:05.123456789Z")).toBe(ms + 123);
    expect(toInstantMs(ms)).toBe(ms);
    expect(toInstantMs(String(ms))).toBe(ms);
  });

  it("不带时区的 YYYY-MM-DD HH:mm:ss 按 UTC 理解，和浏览器本地时区无关", () => {
    expect(toInstantMs("2026-11-07 01:30:05")).toBe(Date.parse("2026-11-07T01:30:05Z"));
  });
});

describe("时间输入：墙钟换成时刻", () => {
  it("按所给时区解释墙钟，和浏览器本地时区无关", () => {
    expect(wallClockToMs("2026-11-07 09:30:00", SH)).toBe(Date.parse("2026-11-07T01:30:00Z"));
    expect(wallClockToMs("2026-11-07 09:30", "UTC")).toBe(Date.parse("2026-11-07T09:30:00Z"));
    expect(wallClockToMs("2026-11-07", SH)).toBe(Date.parse("2026-11-06T16:00:00Z"));
    expect(wallClockToMs("乱写", SH)).toBeNull();
  });

  it("夏令时跳过的墙钟顺延到切换后，重复的墙钟取较早的那个", () => {
    expect(msToWallClock(wallClockToMs("2026-03-08 02:30:00", NY)!, NY)).toBe("2026-03-08 03:30:00");
    expect(wallClockToMs("2026-11-01 01:30:00", NY)).toBe(Date.parse("2026-11-01T05:30:00Z"));
  });

  it("和显示互为逆运算", () => {
    const ms = Date.parse("2026-07-01T13:45:00Z");
    for (const zone of ["UTC", SH, NY]) expect(wallClockToMs(msToWallClock(ms, zone), zone)).toBe(ms);
  });
});

describe("运维监控按市场显示的时间", () => {
  const now = Date.parse("2026-10-16T18:56:10Z");

  it("同一天只写时分，跨天写月-日，后缀按市场时区", () => {
    expect(marketTime("2026-10-16T18:54:00Z", "UTC", now)).toBe("18:54 UTC");
    expect(marketTime("2026-10-16T18:54:07Z", "UTC", now, { seconds: true })).toBe("18:54:07 UTC");
    expect(marketTime("2026-10-15T23:59:00Z", "UTC", now)).toBe("10-15 23:59 UTC");
    // 北京时间已经是 10-17：同一时刻按 A 股时区算是第二天。
    expect(marketTime("2026-10-16T18:54:00Z", SH, now)).toBe("02:54 UTC+8");
    expect(marketTime("2026-10-16T01:31:00Z", SH, now)).toBe("10-16 09:31 UTC+8");
    expect(marketTime(undefined, "UTC", now)).toBe("—");
    expect(marketTime(Date.parse("2026-10-16T18:00:00Z"), "UTC", now)).toBe("18:00 UTC");
  });

  it("按市场时区分日，坐标轴只写时分", () => {
    expect(marketDay(now, "UTC")).toBe("2026-10-16");
    expect(marketDay(now, SH)).toBe("2026-10-17");
    expect(marketClock(Date.parse("2026-10-16T01:31:00Z"), SH)).toBe("09:31");
  });
});
