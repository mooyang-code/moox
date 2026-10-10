import { describe, expect, it } from "vitest";
import { formatDuration, formatCheckedAt, statusLabel } from "./health-display";

describe("health display", () => {
  it("uses friendly Chinese labels", () => {
    expect(statusLabel("degraded")).toBe("需关注");
    expect(statusLabel("down")).toBe("异常");
  });
  it("does not expose invalid timestamps", () => {
    expect(formatCheckedAt("invalid")).toBe("暂无");
  });
  it("keeps disabled and unchecked distinct and uses server observation time for duration", () => {
    expect(statusLabel("disabled")).toBe("已停用");
    expect(statusLabel("unchecked")).toBe("不探测");
    expect(statusLabel("stale")).toBe("未知");
    expect(formatDuration("2026-10-09T12:00:00Z", "2026-10-09T13:05:00Z")).toBe("1 小时 5 分钟");
    expect(formatDuration("bad", "bad")).toBe("暂无");
  });
});
