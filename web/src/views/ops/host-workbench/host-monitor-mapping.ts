import {
  aggregateNetworkRate,
  maxAvailableFilesystemUsage,
  metricValueAvailable,
  type HostMetrics
} from "@/api/modules/host-monitor";
import type { DeploymentHost } from "@/api/admin/types";

export type MonitorViewMode = "cards" | "master";

export interface HostMonitorRow {
  /** 登记主机 ID，也是行的唯一键 */
  key: string;
  hostId: string;
  host: DeploymentHost;
  /** 该主机最近上报的采集器；没有上报时为空 */
  monitor?: HostMetrics;
  /** 中文名，没有登记中文名时回退为主机 ID */
  displayName: string;
  displayAddress: string;
  state: "online" | "offline" | "unmonitored";
  cpuUsage: number | null;
  memoryUsage: number | null;
  filesystemUsage: number | null;
  networkRate: { rx: number; tx: number } | null;
  attention: boolean;
}

function summarize(monitor?: HostMetrics) {
  if (!monitor) {
    return {
      state: "unmonitored" as const,
      cpuUsage: null,
      memoryUsage: null,
      filesystemUsage: null,
      networkRate: null,
      attention: false
    };
  }

  const state = monitor.status === "online" ? ("online" as const) : ("offline" as const);
  const filesystemUsage = maxAvailableFilesystemUsage(monitor.filesystems);
  const networkRate = aggregateNetworkRate(monitor.networks);
  const cpuUsage = metricValueAvailable(monitor.status, monitor.cpu.usage_available) ? Math.round(monitor.cpu.usage) : null;
  const memoryUsage = metricValueAvailable(monitor.status, monitor.memory.percent_available)
    ? Math.round(monitor.memory.percent)
    : null;
  const availableFilesystemUsage =
    metricValueAvailable(monitor.status, filesystemUsage !== null) && filesystemUsage !== null
      ? Math.round(filesystemUsage)
      : null;
  const availableNetworkRate = metricValueAvailable(monitor.status, networkRate !== null) ? networkRate : null;
  const attention = [cpuUsage, memoryUsage, availableFilesystemUsage].some(value => value !== null && value >= 80);

  return {
    state,
    cpuUsage,
    memoryUsage,
    filesystemUsage: availableFilesystemUsage,
    networkRate: availableNetworkRate,
    attention
  };
}

function reportedAt(monitor: HostMetrics) {
  const time = Date.parse(monitor.timestamp);
  return Number.isFinite(time) ? time : 0;
}

// 按登记主机关联采集器：多个采集器（如重装后换了 agent_id）时取最近上报的。
export function buildHostMonitorRows(hosts: DeploymentHost[], monitors: HostMetrics[]): HostMonitorRow[] {
  const latest = new Map<string, HostMetrics>();
  for (const monitor of monitors) {
    if (!monitor.host_id) continue;
    const current = latest.get(monitor.host_id);
    if (!current || reportedAt(monitor) > reportedAt(current)) latest.set(monitor.host_id, monitor);
  }
  return hosts.map(host => {
    const monitor = latest.get(host.host_id);
    return {
      key: host.host_id,
      hostId: host.host_id,
      host,
      monitor,
      displayName: host.label || host.host_id,
      displayAddress: host.address || "--",
      ...summarize(monitor)
    };
  });
}

// 没有登记主机 ID、或主机 ID 不在登记里的采集器只统计数量，不进入主机列表。
export function countUnregisteredAgents(hosts: DeploymentHost[], monitors: HostMetrics[]) {
  const registered = new Set(hosts.map(host => host.host_id));
  return new Set(monitors.filter(monitor => !registered.has(monitor.host_id)).map(monitor => monitor.agent_id)).size;
}

export function normalizeMonitorViewMode(value: string | null): MonitorViewMode {
  return value === "master" ? "master" : "cards";
}
