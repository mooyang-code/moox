import { describe, expect, it } from "vitest";
import { alertLink, formatCheckedAt, statusLabel } from "./health-display";

describe("health display", () => {
  it("uses friendly Chinese labels", () => {
    expect(statusLabel("degraded")).toBe("需关注");
    expect(statusLabel("down")).toBe("异常");
    expect(statusLabel("disabled")).toBe("已停用");
    expect(statusLabel("unchecked")).toBe("不探测");
    expect(statusLabel("stale")).toBe("未知");
  });
  it("does not expose invalid timestamps", () => {
    expect(formatCheckedAt("invalid")).toBe("暂无");
  });
  it("links each alert to the page that handles it", () => {
    expect(alertLink({ type: "host", host_id: "control", agent_id: "pwU2" })).toEqual({
      href: "#/ops/hosts?tab=monitor&host_id=control",
      label: "查看主机"
    });
    expect(alertLink({ type: "dataset", producer: "strategy" }).href).toBe("#/strategy/tasks");
    expect(alertLink({ type: "business", kind: "market_canary" }).label).toBe("查看采集结果");
    expect(alertLink({ type: "component", host_id: "control", component_id: "collector" }).href).toBe(
      "#/ops/deployments?tab=services&host_id=control&component_id=collector"
    );
  });
});
