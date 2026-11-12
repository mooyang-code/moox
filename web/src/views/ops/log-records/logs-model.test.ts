import { describe, expect, it } from "vitest";
import { fieldEntries, firstLine, levelLabel, levelTone, mergedCount } from "./logs-model";

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
