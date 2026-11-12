import { describe, expect, it } from "vitest";
import {
  aroundWindow,
  barsPeak,
  fieldEntries,
  firstLine,
  levelLabel,
  levelTone,
  logBars,
  mergedCount,
  rangeWindow
} from "./logs-model";

describe("重要日志的展示", () => {
  it("关键事件叫“事件”，错误和致命标红", () => {
    expect(levelLabel({ level: "info", event: "process_started" })).toBe("事件");
    expect(levelLabel({ level: "error", event: "order_rejected" })).toBe("错误");
    expect(levelLabel({ level: "fatal" })).toBe("致命");
    expect(levelLabel({ level: "panic" })).toBe("致命");
    expect(levelTone({ level: "panic", event: "x" })).toBe("error");
    expect(levelLabel({ level: "info" })).toBe("信息");
    expect(levelLabel({})).toBe("--");
    expect(levelTone({ level: "error" })).toBe("error");
    expect(levelTone({ level: "fatal" })).toBe("error");
    expect(levelTone({ level: "info", event: "process_started" })).toBe("event");
  });

  it("列表只显示正文第一行", () => {
    expect(firstLine("写入失败\n堆栈第一行\n堆栈第二行")).toBe("写入失败");
    expect(firstLine(undefined)).toBe("");
  });

  it("合并条数兼容数字与字符串，无效值按 0", () => {
    expect(mergedCount({ merged_count: 3 })).toBe(3);
    expect(mergedCount({ merged_count: "12" })).toBe(12);
    expect(mergedCount({ merged_count: "x" })).toBe(0);
    expect(mergedCount({})).toBe(0);
  });

  it("字段按键名排序", () => {
    expect(fieldEntries({ fields: { view: "v1", account: "a1" } })).toEqual([
      ["account", "a1"],
      ["view", "v1"]
    ]);
    expect(fieldEntries({})).toEqual([]);
  });
});

describe("重要日志的时间范围与分布", () => {
  const HOUR = 3_600_000;
  const now = Date.parse("2026-11-12T10:20:00Z");

  it("预设范围的终点是下一个整点", () => {
    expect(rangeWindow("24h", now)).toEqual({
      startMs: Date.parse("2026-11-11T11:00:00Z"),
      endMs: Date.parse("2026-11-12T11:00:00Z")
    });
    const week = rangeWindow("7d", now);
    expect((week.endMs - week.startMs) / HOUR).toBe(168);
    expect((rangeWindow("30d", now).endMs - rangeWindow("30d", now).startMs) / HOUR).toBe(720);
  });

  it("看前后：以这条日志为中心各 5 分钟，这条本身在窗口内", () => {
    const at = Date.parse("2026-11-12T09:55:03.494Z");
    const window = aroundWindow("2026-11-12T09:55:03.494700953Z");
    expect(window).not.toBeNull();
    expect(window!.startMs).toBe(at - 300_000);
    // 终点不含：多出 1 毫秒，正好在 5 分钟后那一刻的记录也算在内。
    expect(window!.endMs).toBe(at + 300_000 + 1);
    expect(aroundWindow("不是时间")).toBeNull();
    expect(aroundWindow(undefined)).toBeNull();
  });

  it("24 小时内每小时一根，没有记录的小时也占位", () => {
    const window = rangeWindow("24h", now);
    const bars = logBars(
      [
        { start_at: "2026-11-12T04:00:00Z", errors: 2, events: 1 },
        { start_at: "2026-11-12T10:00:00Z", errors: 0, events: 3 },
        { start_at: "2026-11-01T00:00:00Z", errors: 9, events: 9 },
        { start_at: "坏的", errors: 1 }
      ],
      window
    );
    expect(bars).toHaveLength(24);
    expect(bars[0].startMs).toBe(window.startMs);
    const at = (iso: string) => bars.find(bar => bar.startMs === Date.parse(iso));
    expect(at("2026-11-12T04:00:00Z")).toMatchObject({ errors: 2, events: 1 });
    expect(at("2026-11-12T10:00:00Z")).toMatchObject({ errors: 0, events: 3 });
    expect(bars.reduce((sum, bar) => sum + bar.errors + bar.events, 0)).toBe(6);
    expect(barsPeak(bars)).toBe(3);
  });

  it("7 天按 6 小时、30 天按天归并", () => {
    const week = logBars(
      [
        { start_at: "2026-11-12T01:00:00Z", errors: 1 },
        { start_at: "2026-11-12T05:00:00Z", events: 2 },
        { start_at: "2026-11-12T06:00:00Z", errors: 4 }
      ],
      rangeWindow("7d", now)
    );
    expect(week.find(bar => bar.startMs === Date.parse("2026-11-12T00:00:00Z"))).toMatchObject({ errors: 1, events: 2 });
    expect(week.find(bar => bar.startMs === Date.parse("2026-11-12T06:00:00Z"))).toMatchObject({ errors: 4, events: 0 });
    // 首尾两根裁到窗口内，中间的都是整 6 小时。
    const weekWindow = rangeWindow("7d", now);
    expect(week[0].startMs).toBe(weekWindow.startMs);
    expect(week[week.length - 1].endMs).toBe(weekWindow.endMs);
    expect(week.slice(1, -1).every(bar => bar.endMs - bar.startMs === 6 * HOUR)).toBe(true);
    expect(week[0].endMs - week[0].startMs).toBe(HOUR);
    const monthWindow = rangeWindow("30d", now);
    const month = logBars([{ start_at: "2026-11-12T09:00:00Z", errors: 1 }], monthWindow);
    expect(month[0].startMs).toBe(monthWindow.startMs);
    expect(month[month.length - 1]).toMatchObject({
      startMs: Date.parse("2026-11-12T00:00:00Z"),
      endMs: monthWindow.endMs,
      errors: 1
    });
    expect(month.slice(1, -1).every(bar => bar.endMs - bar.startMs === 24 * HOUR)).toBe(true);
  });

  it("窗口不足一小时不画；没有记录时峰值按 1", () => {
    expect(logBars([{ start_at: "2026-11-12T09:00:00Z", errors: 1 }], aroundWindow("2026-11-12T09:55:00Z")!)).toEqual([]);
    expect(barsPeak([])).toBe(1);
  });
});
