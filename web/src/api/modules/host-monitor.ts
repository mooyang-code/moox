import { callControl } from '@/api/admin/http';
import {
  aggregateNetworkRate,
  filesystemUsageAvailable,
  maxAvailableFilesystemUsage,
  memoryUsageAvailable,
} from './host-monitor-mapping';

export { aggregateNetworkRate, historyHasRenderableData, maxAvailableFilesystemUsage, metricValueAvailable } from './host-monitor-mapping';

export interface CPUMetrics {
  usage: number;
  usage_available: boolean;
  cores: number;
}

export interface MemoryMetrics {
  total: number;
  used: number;
  available: number;
  percent: number;
  percent_available: boolean;
}

export interface FilesystemMetrics {
  device: string;
  mountpoint: string;
  fs_type: string;
  total: number;
  used: number;
  available: number;
  percent: number;
  percent_available: boolean;
  read_only: boolean;
}

export interface DiskMetrics {
  device: string;
  read_bytes_per_second: number;
  write_bytes_per_second: number;
  read_iops: number;
  write_iops: number;
  utilization_percent: number;
  rate_available: boolean;
}

export interface NetworkSpeed {
  device: string;
  operstate: string;
  rx_speed: number;
  tx_speed: number;
  receive_errors_total: number;
  transmit_errors_total: number;
  rate_available: boolean;
}

export interface HostMetrics {
  host_id: string;
  host_name: string;
  address: string;
  status: 'online' | 'offline' | 'error';
  timestamp: string;
  cpu: CPUMetrics;
  memory: MemoryMetrics;
  filesystems: FilesystemMetrics[];
  disks: DiskMetrics[];
  networks: NetworkSpeed[];
  storage_available: boolean;
}

export interface HistoryPoint {
  timestamp: string;
  cpu_usage: number;
  cpu_available: boolean;
  memory_percent: number;
  memory_available: boolean;
  disk_percent: number;
  disk_available: boolean;
  network_rx_speed: number;
  network_tx_speed: number;
  network_available: boolean;
}

interface HostSnapshot {
  cpu?: { logical_cores?: number; usage_percent?: number; usage_available?: boolean };
  memory?: { total_bytes?: number; used_bytes?: number; available_bytes?: number; usage_percent?: number };
  filesystems?: Array<{ device?: string; mountpoint?: string; fs_type?: string; total_bytes?: number; used_bytes?: number; available_bytes?: number; usage_percent?: number; read_only?: boolean }>;
  disks?: Array<{ device?: string; read_bytes_per_second?: number; write_bytes_per_second?: number; read_iops?: number; write_iops?: number; utilization_percent?: number; rate_available?: boolean }>;
  networks?: Array<{ device?: string; operstate?: string; receive_bytes_per_second?: number; transmit_bytes_per_second?: number; receive_errors_total?: number; transmit_errors_total?: number; rate_available?: boolean }>;
}

interface HostAgent {
  agent_id?: string;
  hostname?: string;
  last_seen_at?: string;
  archived?: boolean;
  snapshot?: HostSnapshot;
}

interface HostHistoryPoint {
  agent_id?: string;
  observed_at?: string;
  snapshot?: HostSnapshot;
}

export const getCurrentMetrics = () => {
  return callControl<Record<string, never>, { agents?: HostAgent[]; storage_available?: boolean; data_gap?: boolean }>('monitor', 'ListHostAgents', {}).then((response) => ({
    metrics: (response.agents ?? []).map((agent) => toHostMetrics(agent, response.storage_available !== false)),
    storage_available: response.storage_available !== false,
    data_gap: response.data_gap === true,
  }));
};

export const getHistoryMetrics = (hostID: string, duration = '1h') => {
  const end = new Date();
  const start = new Date(end.getTime() - durationMilliseconds(duration));
  return callControl<
    { agent_id: string; start_at: string; end_at: string; limit: number },
    { points?: HostHistoryPoint[]; storage_available?: boolean; data_gap?: boolean }
  >('monitor', 'QueryHostMetricHistory', {
    agent_id: hostID,
    start_at: start.toISOString(),
    end_at: end.toISOString(),
    limit: 500,
  }).then((response) => ({
    history: (response.points ?? []).map(toHistoryPoint),
    storage_available: response.storage_available !== false,
    data_gap: response.data_gap === true,
  }));
};

export const toHostMetrics = (agent: HostAgent, storageAvailable = true): HostMetrics => {
  const snapshot = agent.snapshot ?? {};
  const cpu = snapshot.cpu ?? {};
  const memory = snapshot.memory ?? {};
  return {
    host_id: agent.agent_id ?? '',
    host_name: agent.hostname ?? agent.agent_id ?? 'unknown',
    address: agent.agent_id ?? '',
    status: agent.archived ? 'offline' : isFresh(agent.last_seen_at) ? 'online' : 'offline',
    timestamp: agent.last_seen_at ?? '',
    cpu: {
      usage: cpu.usage_percent ?? 0,
      usage_available: cpu.usage_available === true,
      cores: cpu.logical_cores ?? 0,
    },
    memory: {
      total: memory.total_bytes ?? 0,
      used: memory.used_bytes ?? 0,
      available: memory.available_bytes ?? 0,
      percent: memory.usage_percent ?? 0,
      percent_available: memoryUsageAvailable(memory.total_bytes, snapshot.memory !== undefined),
    },
    filesystems: (snapshot.filesystems ?? []).map((item) => ({
      device: item.device ?? '',
      mountpoint: item.mountpoint ?? '',
      fs_type: item.fs_type ?? '',
      total: item.total_bytes ?? 0,
      used: item.used_bytes ?? 0,
      available: item.available_bytes ?? 0,
      percent: item.usage_percent ?? 0,
      percent_available: filesystemUsageAvailable(item.device, item.mountpoint, item.total_bytes),
      read_only: item.read_only === true,
    })),
    disks: (snapshot.disks ?? []).map((item) => ({
      device: item.device ?? '',
      read_bytes_per_second: item.read_bytes_per_second ?? 0,
      write_bytes_per_second: item.write_bytes_per_second ?? 0,
      read_iops: item.read_iops ?? 0,
      write_iops: item.write_iops ?? 0,
      utilization_percent: item.utilization_percent ?? 0,
      rate_available: item.rate_available === true,
    })),
    networks: (snapshot.networks ?? []).map((item) => ({
      device: item.device ?? '',
      operstate: item.operstate ?? 'unknown',
      rx_speed: item.receive_bytes_per_second ?? 0,
      tx_speed: item.transmit_bytes_per_second ?? 0,
      receive_errors_total: item.receive_errors_total ?? 0,
      transmit_errors_total: item.transmit_errors_total ?? 0,
      rate_available: item.rate_available === true,
    })),
    storage_available: storageAvailable,
  };
};

const toHistoryPoint = (point: HostHistoryPoint): HistoryPoint => {
  const metric = toHostMetrics({ agent_id: point.agent_id, snapshot: point.snapshot, last_seen_at: point.observed_at });
  const filesystemUsage = maxAvailableFilesystemUsage(metric.filesystems);
  const networkRate = aggregateNetworkRate(metric.networks);
  return {
    timestamp: point.observed_at ?? '',
    cpu_usage: metric.cpu.usage,
    cpu_available: metric.cpu.usage_available,
    memory_percent: metric.memory.percent,
    memory_available: metric.memory.percent_available,
    disk_percent: filesystemUsage ?? 0,
    disk_available: filesystemUsage !== null,
    network_rx_speed: networkRate?.rx ?? 0,
    network_tx_speed: networkRate?.tx ?? 0,
    network_available: networkRate !== null,
  };
};

const isFresh = (value?: string) => !!value && Number.isFinite(Date.parse(value)) && Date.now() - Date.parse(value) < 60_000;

const durationMilliseconds = (value: string) => {
  const match = /^(\d+)([hdm])$/.exec(value);
  if (!match) return 60 * 60 * 1000;
  return Number(match[1]) * ({ m: 60_000, h: 3_600_000, d: 86_400_000 } as Record<string, number>)[match[2]];
};

export const formatBytes = (bytes: number): string => {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B';
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
  const index = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), sizes.length - 1);
  return `${Math.round((bytes / 1024 ** index) * 100) / 100} ${sizes[index]}`;
};

export const formatBytesPerSecond = (bytesPerSecond: number): string => `${formatBytes(bytesPerSecond)}/s`;
