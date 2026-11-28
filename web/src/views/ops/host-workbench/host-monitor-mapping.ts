import {
  aggregateNetworkRate,
  maxAvailableFilesystemUsage,
  metricValueAvailable,
  type HostAlertThresholds,
  type HostMetrics
} from "@/api/modules/host-monitor";
import type { DeploymentHost } from "@/api/admin/types";

export type MonitorViewMode = "cards" | "master";

export interface HostMonitorRow {
  /** 登记主机 ID，也是行的唯一键 */
  key: string;
  hostId: string;
  host: DeploymentHost;
  /** 该主机最近一次上报；没有上报时为空 */
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

function exceedsThreshold(value: number | null, threshold: number | undefined): boolean {
  return value !== null && threshold !== undefined && threshold > 0 && value >= threshold;
}

function summarize(monitor?: HostMetrics, thresholds?: HostAlertThresholds) {
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
  const cpuRaw = metricValueAvailable(monitor.status, monitor.cpu.usage_available) ? monitor.cpu.usage : null;
  const memoryRaw = metricValueAvailable(monitor.status, monitor.memory.percent_available) ? monitor.memory.percent : null;
  const filesystemRaw =
    metricValueAvailable(monitor.status, filesystemUsage !== null) && filesystemUsage !== null ? filesystemUsage : null;
  const cpuUsage = cpuRaw !== null ? Math.round(cpuRaw) : null;
  const memoryUsage = memoryRaw !== null ? Math.round(memoryRaw) : null;
  const availableFilesystemUsage = filesystemRaw !== null ? Math.round(filesystemRaw) : null;
  const availableNetworkRate = metricValueAvailable(monitor.status, networkRate !== null) ? networkRate : null;
  const attention =
    exceedsThreshold(cpuRaw, thresholds?.cpu) ||
    exceedsThreshold(memoryRaw, thresholds?.memory) ||
    exceedsThreshold(filesystemRaw, thresholds?.disk);

  return {
    state,
    cpuUsage,
    memoryUsage,
    filesystemUsage: availableFilesystemUsage,
    networkRate: availableNetworkRate,
    attention
  };
}

// 每台登记主机一行：Monitor 按主机 ID 记录上报，直接按 host_id 对上。
export function buildHostMonitorRows(
  hosts: DeploymentHost[],
  monitors: HostMetrics[],
  thresholds?: HostAlertThresholds
): HostMonitorRow[] {
  const byHost = new Map(monitors.map(monitor => [monitor.host_id, monitor]));
  return hosts.map(host => {
    const monitor = byHost.get(host.host_id);
    return {
      key: host.host_id,
      hostId: host.host_id,
      host,
      monitor,
      displayName: host.label || host.host_id,
      displayAddress: host.address || "--",
      ...summarize(monitor, thresholds)
    };
  });
}

export function normalizeMonitorViewMode(value: string | null): MonitorViewMode {
  return value === "master" ? "master" : "cards";
}
