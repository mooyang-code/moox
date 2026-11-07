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
    expect(alertLink({ type: "business", kind: "event_center_attention" })).toEqual({
      href: "#/settings/events?tab=executions",
      label: "查看事件中心执行记录"
    });
    expect(alertLink({ type: "business", kind: "collector_attention", attention_kind: "delist_readonly_key_missing" })).toEqual({
      href: "#/settings/secrets",
      label: "去录入币安只读 Key"
    });
    expect(alertLink({ type: "business", kind: "collector_attention", attention_kind: "delist_source_failed" }).href).not.toBe(
      "#/settings/secrets"
    );
    // 只认 attention_kind，不再从告警编号里截取
    expect(alertLink({ type: "business", kind: "collector_attention" }).href).not.toBe("#/settings/secrets");
    expect(alertLink({ type: "component", host_id: "control", component_id: "collector" }).href).toBe(
      "#/ops/deployments?tab=services&host_id=control&component_id=collector"
    );
  });
});
