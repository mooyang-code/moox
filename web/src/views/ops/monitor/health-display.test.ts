import { describe, expect, it } from "vitest";
import { alertLink, alertNote, statusLabel } from "./health-display";

describe("health display", () => {
  it("uses friendly Chinese labels", () => {
    expect(statusLabel("degraded")).toBe("需关注");
    expect(statusLabel("down")).toBe("异常");
    expect(statusLabel("disabled")).toBe("已停用");
    expect(statusLabel("unchecked")).toBe("不探测");
    expect(statusLabel("stale")).toBe("未知");
  });
  it("links each alert to the page that handles it", () => {
    expect(alertLink({ type: "host", host_id: "control", agent_id: "pwU2" })).toEqual({
      href: "#/ops/hosts?tab=monitor&host_id=control",
      label: "查看主机"
    });
    expect(alertLink({ type: "dataset", producer: "strategy" }).href).toBe("#/strategy/tasks");
    // 采集数据集的告警直达这个数据集的采集结果，并带上它所在的空间（打开前先切过去）
    expect(alertLink({ type: "dataset", producer: "collector", dataset_id: "ds_5m", freq: "5m", space_id: "stockcn" })).toEqual({
      href: "#/collector/tasks?tab=results&dataset=ds_5m&freq=5m",
      label: "查看采集结果",
      spaceId: "stockcn"
    });
    expect(alertLink({ type: "business", kind: "market_canary" }).label).toBe("查看采集结果");
    // 事件中心关注项按种类跳转；认不出种类时看全部处理记录
    const eventCenter = (attention_kind?: string) =>
      alertLink({ type: "business", kind: "event_center_attention", attention_kind });
    expect(eventCenter("execution_failed").href).toBe("#/events/executions?status=failed");
    // 系统动作处理失败的记录不一定是“失败”状态（善后未完成、步骤失败等），不带筛选
    expect(eventCenter("action_failed").href).toBe("#/events/executions");
    expect(eventCenter("delist_detected").href).toBe("#/events/delist");
    expect(eventCenter("something_new")).toEqual({ href: "#/events/executions", label: "查看处理记录" });
    expect(eventCenter().href).toBe("#/events/executions");
    // 缺币安只读 Key：首页跳监控页看说明；说明分三种情况写，被禁用的那种给密钥页入口
    const readonlyMissing = { type: "business", kind: "collector_attention", attention_kind: "delist_readonly_key_missing" };
    expect(alertLink(readonlyMissing).href).toBe("#/ops/monitor");
    const note = alertNote(readonlyMissing);
    expect(note?.link).toEqual({ href: "#/settings/secrets", label: "已禁用的话到密钥管理页启用" });
    const [disabled, missing, deleted, manual] = note?.lines ?? [];
    expect(disabled).toContain("被禁用");
    expect(disabled).toContain("启用");
    expect(missing).toContain("moox-cli config edit");
    expect(missing).toContain("正式环境");
    expect(missing).toContain("名称填 readonly");
    expect(missing).toContain("moox-cli setup apply");
    expect(deleted).toContain("删除过");
    expect(deleted).toContain("setup apply 不会重建");
    expect(manual).toContain("moox-cli collector delist register");
    expect(manual).toContain("事件中心");
    expect(note?.lines.join("")).not.toContain("生产环境");
    expect(alertNote({ type: "business", kind: "collector_attention", attention_kind: "delist_source_failed" })).toBeNull();
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
