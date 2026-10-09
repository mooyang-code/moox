import { describe, expect, it } from "vitest";
import type { HealthComponent } from "@/api/monitor";
import {
  buildComponentMatrix,
  formatLag,
  formatSeconds,
  formatSince,
  locateRoute,
  overallSentence,
  reporterLabel,
  stageCounts,
  statusColor,
  statusLabel,
  targetLabel
} from "./monitor-display";

describe("monitor display helpers", () => {
  it("labels the six health states and shows none as 不探测", () => {
    expect(["healthy", "degraded", "down", "unknown", "disabled", "unchecked"].map(statusLabel)).toEqual([
      "正常",
      "需关注",
      "异常",
      "未知",
      "已停用",
      "不探测"
    ]);
    expect(statusLabel("whatever")).toBe("未知");
    expect(statusColor("down")).toBe("red");
    expect(reporterLabel("")).toBe("不上报运行指标");
    expect(reporterLabel("never_reported")).toBe("从未上报");
  });

  it("formats durations, int64 lags and elapsed time", () => {
    expect(formatSeconds(45)).toBe("45 秒");
    expect(formatSeconds(3900)).toBe("1 小时 5 分钟");
    expect(formatSeconds(3 * 86400 + 7200)).toBe("3 天 2 小时");
    expect(formatLag("10800")).toBe("3 小时");
    expect(formatLag(0)).toBe("—");
    expect(formatSince("2026-10-09T03:55:00Z", new Date("2026-10-09T04:00:00Z"))).toBe("5 分钟");
    expect(formatSince("", new Date())).toBe("");
  });

  it("describes alert targets including host alerts", () => {
    const components: HealthComponent[] = [{ host_id: "storage", component_id: "storage-primary", name: "存储主服务" }];
    expect(targetLabel({ kind: "component", host_id: "storage", component_id: "storage-primary" }, components)).toBe(
      "存储主服务 @ storage"
    );
    expect(targetLabel({ kind: "host", host_id: "storage" })).toBe("主机 storage");
    expect(targetLabel({ kind: "dataset", dataset_id: "dataset_crypto_kline", frequency: "1m" })).toBe(
      "dataset_crypto_kline · 1m"
    );
  });

  it("locates alerts on the deployment, data and host pages", () => {
    const hosts = [{ host_id: "control", agent_id: "aB3x" }];
    expect(locateRoute({ target: { kind: "host", host_id: "control" } }, hosts)).toEqual({
      path: "/ops/hosts",
      query: { tab: "monitor", agent: "aB3x" }
    });
    expect(locateRoute({ target: { kind: "component", host_id: "storage" } })).toEqual({
      path: "/ops/services",
      query: { tab: "instances" }
    });
    expect(locateRoute({ target: { kind: "dataset" }, stage: "factor" })).toEqual({ path: "/factor/tasks" });
    expect(locateRoute({ target: { kind: "dataset" }, stage: "collect" })).toEqual({
      path: "/collector/tasks",
      query: { tab: "results" }
    });
    expect(locateRoute({ target: { kind: "business" } })).toBeUndefined();
  });

  it("builds the component x host matrix with only deployed cells and abnormal rows first", () => {
    const matrix = buildComponentMatrix([
      { host_id: "storage", component_id: "storage-primary", name: "存储主服务", status: "down" },
      { host_id: "control", component_id: "monitor", name: "监控服务", status: "healthy" },
      { host_id: "control", component_id: "host-agent", name: "主机采集器", status: "healthy" },
      { host_id: "storage", component_id: "host-agent", name: "主机采集器", status: "degraded" }
    ]);
    expect(matrix.hosts).toEqual(["control", "storage"]);
    expect(matrix.rows.map(row => row.componentId)).toEqual(["storage-primary", "host-agent", "monitor"]);
    expect(Object.keys(matrix.rows[0].cells)).toEqual(["storage"]);
    expect(matrix.rows[1].status).toBe("degraded");
  });

  it("summarizes the overall state in one sentence", () => {
    expect(overallSentence({ summary: { alerts: 2, attention: 3 } })).toBe("有 2 条告警正在触发，3 项需关注");
    expect(overallSentence({ summary: { attention: 1 } })).toBe("没有正在触发的告警，但有 1 项需关注");
    expect(overallSentence({ summary: {} })).toBe("一切正常");
    expect(stageCounts({ datasets: [{ status: "down" }, { status: "healthy" }] })).toEqual({ total: 2, attention: 1 });
  });
});
