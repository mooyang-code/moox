import { describe, expect, it } from "vitest";
import { formatRetention, retentionSourceLabel } from "./retention";

describe("retention", () => {
  it("formats Storage retention values", () => {
    expect(formatRetention("168h")).toBe("7 天");
    expect(formatRetention("48h")).toBe("2 天");
    expect(formatRetention("36h")).toBe("36 小时");
    expect(formatRetention("forever")).toBe("永久");
    expect(formatRetention("")).toBe("-");
    expect(formatRetention(undefined)).toBe("-");
  });

  it("labels the retention source", () => {
    expect(retentionSourceLabel("default")).toBe("全局默认");
    expect(retentionSourceLabel("space")).toBe("空间配置");
    expect(retentionSourceLabel("record")).toBe("记录型不过期");
    expect(retentionSourceLabel("")).toBe("-");
  });
});
