import {
  aggregateNetworkRate,
  maxAvailableFilesystemUsage,
  metricValueAvailable,
  type HostMetrics,
} from '@/api/modules/host-monitor';
import type { SSHHost } from '@/api/modules/ssh';

export type MonitorViewMode = 'cards' | 'master';

export interface HostMonitorRow {
  key: string;
  ssh?: SSHHost;
  monitor?: HostMetrics;
  match: 'address' | 'unique_name' | 'unmatched';
  displayName: string;
  displayAddress: string;
  state: 'online' | 'offline' | 'unmonitored';
  cpuUsage: number | null;
  memoryUsage: number | null;
  filesystemUsage: number | null;
  networkRate: { rx: number; tx: number } | null;
  attention: boolean;
}

function normalize(value?: string) {
  return (value || '').trim().toLowerCase();
}

function summarize(monitor?: HostMetrics) {
  if (!monitor) {
    return {
      state: 'unmonitored' as const,
      cpuUsage: null,
      memoryUsage: null,
      filesystemUsage: null,
      networkRate: null,
      attention: false,
    };
  }

  const state = monitor.status === 'online' ? 'online' as const : 'offline' as const;
  const filesystemUsage = maxAvailableFilesystemUsage(monitor.filesystems);
  const networkRate = aggregateNetworkRate(monitor.networks);
  const cpuUsage = metricValueAvailable(monitor.status, monitor.cpu.usage_available) ? Math.round(monitor.cpu.usage) : null;
  const memoryUsage = metricValueAvailable(monitor.status, monitor.memory.percent_available) ? Math.round(monitor.memory.percent) : null;
  const availableFilesystemUsage = metricValueAvailable(monitor.status, filesystemUsage !== null) && filesystemUsage !== null ? Math.round(filesystemUsage) : null;
  const availableNetworkRate = metricValueAvailable(monitor.status, networkRate !== null) ? networkRate : null;
  const attention = [cpuUsage, memoryUsage, availableFilesystemUsage].some((value) => value !== null && value >= 80);

  return {
    state,
    cpuUsage,
    memoryUsage,
    filesystemUsage: availableFilesystemUsage,
    networkRate: availableNetworkRate,
    attention,
  };
}

function createRow(monitor: HostMetrics | undefined, ssh: SSHHost | undefined, match: HostMonitorRow['match']): HostMonitorRow {
  const identity = monitor?.host_id || ssh?.id || ssh?.address || 'unknown';
  return {
    key: monitor ? `monitor:${identity}` : `ssh:${identity}`,
    ssh,
    monitor,
    match,
    displayName: ssh?.name || monitor?.host_name || monitor?.host_id || '未知主机',
    displayAddress: ssh?.address || monitor?.address || monitor?.host_id || '--',
    ...summarize(monitor),
  };
}

export function buildHostMonitorRows(monitors: HostMetrics[], sshHosts: SSHHost[]): HostMonitorRow[] {
  const rows: HostMonitorRow[] = [];
  const usedSSH = new Set<number>();
  const monitorNameCount = new Map<string, number>();
  const sshNameCount = new Map<string, number>();

  for (const monitor of monitors) {
    const name = normalize(monitor.host_name);
    monitorNameCount.set(name, (monitorNameCount.get(name) || 0) + 1);
  }
  for (const host of sshHosts) {
    const name = normalize(host.name);
    sshNameCount.set(name, (sshNameCount.get(name) || 0) + 1);
  }

  for (const monitor of monitors) {
    const addresses = new Set([normalize(monitor.address), normalize(monitor.host_id)].filter(Boolean));
    const addressMatches = sshHosts.filter((host) => host.id !== undefined && !usedSSH.has(host.id) && addresses.has(normalize(host.address)));
    let ssh = addressMatches.length === 1 ? addressMatches[0] : undefined;
    let match: HostMonitorRow['match'] = ssh ? 'address' : 'unmatched';

    const name = normalize(monitor.host_name);
    if (!ssh && name && monitorNameCount.get(name) === 1 && sshNameCount.get(name) === 1) {
      ssh = sshHosts.find((host) => host.id !== undefined && !usedSSH.has(host.id) && normalize(host.name) === name);
      if (ssh) match = 'unique_name';
    }

    if (ssh?.id !== undefined) usedSSH.add(ssh.id);
    rows.push(createRow(monitor, ssh, match));
  }

  for (const host of sshHosts) {
    if (host.id !== undefined && usedSSH.has(host.id)) continue;
    rows.push(createRow(undefined, host, 'unmatched'));
  }

  return rows;
}

export function normalizeMonitorViewMode(value: string | null): MonitorViewMode {
  return value === 'master' ? 'master' : 'cards';
}
