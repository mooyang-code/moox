import { describe, expect, it } from "vitest";
import { formatCheckedAt, statusLabel } from "./health-display";

describe("health display", () => {
  it("uses friendly Chinese labels", () => { expect(statusLabel("degraded")).toBe("需关注"); expect(statusLabel("down")).toBe("异常"); });
  it("does not expose invalid timestamps", () => { expect(formatCheckedAt("invalid")).toBe("暂无"); });
});
