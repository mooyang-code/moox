import { describe, expect, it } from "vitest";
import { alertLink, alertNote, statusLabel } from "./health-display";

describe("health display", () => {
  it("uses friendly Chinese labels", () => {
    expect(statusLabel("degraded")).toBe("需关注");
    expect(statusLabel("down")).toBe("异常");
    expect(statusLabel("disabled")).toBe("已停用");
    expect(statusLabel("unchecked")).toBe("不检查");
    expect(statusLabel("stale")).toBe("未知");
  });
  it("links each alert to the page that handles it", () => {
    expect(alertLink({ type: "host", host_id: "control" })).toEqual({
      href: "#/ops/hosts?tab=monitor&host_id=control",
      label: "查看主机"
    });
    expect(alertLink({ type: "dataset", producer: "strategy" }).href).toBe("#/strategy/tasks");
    // 采集数据集的告警直达这个数据集的采集结果，并带上它所在的空间（打开前先切过去）
    expect(
      alertLink({ type: "dataset", producer: "collector", dataset_id: "ds_5m", frequency: "5m", space_id: "stockcn" })
    ).toEqual({
      href: "#/collector/tasks?tab=results&dataset=ds_5m&freq=5m",
      label: "查看采集结果",
      spaceId: "stockcn"
    });
    expect(alertLink({ type: "business", kind: "market_canary" }).label).toBe("查看采集结果");
    expect(alertLink({ type: "business", kind: "attention", component_id: "trade", attention_kind: "auto_paused" }).href).toBe(
      "#/trading/live"
    );
    // 下架处理的两种关注项去下架名单页
    const tradeAttention = (attention_kind?: string) =>
      alertLink({ type: "business", kind: "attention", component_id: "trade", attention_kind });
    expect(tradeAttention("delist_detected")).toEqual({ href: "#/trading/blocked", label: "查看下架处理" });
    expect(tradeAttention("delist_failed").href).toBe("#/trading/blocked");
    // 缺币安只读 Key：首页跳监控页看说明；说明直接引导在密钥管理页启用或新建
    const readonlyMissing = {
      type: "business",
      kind: "attention",
      component_id: "collector",
      attention_kind: "delist_readonly_key_missing"
    };
    expect(alertLink(readonlyMissing).href).toBe("#/ops/monitor");
    const note = alertNote(readonlyMissing);
    expect(note?.link).toEqual({ href: "#/settings/secrets", label: "前往密钥管理页配置" });
    const [disabled, missing, manual] = note?.lines ?? [];
    expect(disabled).toContain("被禁用");
    expect(disabled).toContain("启用");
    expect(missing).toContain("新建密钥");
    expect(missing).toContain("binance");
    expect(missing).toContain("生产环境");
    expect(missing).toContain("只读密钥");
    expect(manual).toContain("moox-cli collector delist register");
    expect(manual).toContain("下架名单页");
    expect(note?.lines.join("")).not.toContain("config edit");
    expect(note?.lines.join("")).not.toContain("setup init");
    expect(
      alertNote({ type: "business", kind: "attention", component_id: "collector", attention_kind: "delist_source_failed" })
    ).toBeNull();
    expect(
      alertLink({ type: "business", kind: "attention", component_id: "collector", attention_kind: "delist_source_failed" }).href
    ).not.toBe("#/settings/secrets");
    // 只认 attention_kind，不再从告警编号里截取
    expect(alertLink({ type: "business", kind: "attention", component_id: "collector" }).href).not.toBe("#/settings/secrets");
    expect(alertLink({ type: "component", host_id: "control", component_id: "collector" }).href).toBe(
      "#/ops/deployments?tab=services&host_id=control&component_id=collector"
    );
  });
});
