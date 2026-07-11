import { callControl } from '@/api/admin/http';

// ========== 类型定义 ==========

// 主机监控指标
export interface HostMetrics {
  host_id: string;
  host_name: string;
  address: string;
  status: 'online' | 'offline' | 'error';
  error_msg?: string;
  timestamp: string;
  cpu: CPUMetrics;
  memory: MemoryMetrics;
  disks: DiskMetrics[];
  networks: NetworkSpeed[];
  load: LoadMetrics;
  storage_available: boolean;
}

export interface CPUMetrics {
  usage: number;  // CPU使用率（百分比）
  usage_available: boolean;
  cores: number;  // CPU核心数
}

export interface MemoryMetrics {
  total: number;     // 总内存（字节）
  used: number;      // 已用内存（字节）
  available: number; // 可用内存（字节）
  percent: number;   // 使用率（百分比）
  percent_available: boolean;
}

export interface DiskMetrics {
  device: string;
  mountpoint: string;
  total: number;   // 总容量（字节）
  used: number;    // 已用容量（字节）
  percent: number; // 使用率（百分比）
  percent_available: boolean;
}

export interface NetworkSpeed {
  device: string;
  rx_speed: number; // 接收速率（字节/秒）
  tx_speed: number; // 发送速率（字节/秒）
  rate_available: boolean;
}

export interface LoadMetrics {
  load1: number;  // 1分钟平均负载
  load5: number;  // 5分钟平均负载
  load15: number; // 15分钟平均负载
}

// 历史数据点
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

// ========== API接口 ==========

/**
 * 获取当前监控指标
 * @param hostIds 主机ID列表（可选，不传则返回所有启用监控的主机）
 */
export const getCurrentMetrics = (hostIds?: number[]) => {
  void hostIds;
  return callControl<{}, { agents?: HostAgent[]; storage_available?: boolean }>('monitor', 'ListHostAgents', {}).then((response) => ({
    metrics: (response.agents ?? []).map((agent) => toHostMetrics(agent, response.storage_available !== false)),
    storage_available: response.storage_available !== false
  }));
};

/**
 * 获取历史监控数据
 * @param hostID Host Agent 的稳定 agent_id
 * @param duration 时间范围（如 "1h", "24h", "3d"；历史最多保留 3 天）
 */
export const getHistoryMetrics = (hostID: string, duration: string = '1h') => {
  const end = new Date();
  const start = new Date(end.getTime() - durationMilliseconds(duration));
  return callControl<{ agent_id: string; start_at: string; end_at: string; limit: number }, { points?: HostHistoryPoint[]; storage_available?: boolean; data_gap?: boolean }>('monitor', 'QueryHostMetricHistory', {
    agent_id: hostID,
    start_at: start.toISOString(),
    end_at: end.toISOString(),
    limit: 500
  }).then((response) => ({ history: (response.points ?? []).map(toHistoryPoint), storage_available: response.storage_available !== false, data_gap: response.data_gap === true }));
};

interface HostSnapshot {
  cpu?: { logical_cores?: number; usage_percent?: number; usage_available?: boolean };
  memory?: { total_bytes?: number; used_bytes?: number; available_bytes?: number; usage_percent?: number };
  filesystems?: Array<{ device?: string; mountpoint?: string; total_bytes?: number; used_bytes?: number; usage_percent?: number }>;
  networks?: Array<{ device?: string; receive_bytes_per_second?: number; transmit_bytes_per_second?: number; rate_available?: boolean }>;
}
interface HostAgent { agent_id?: string; hostname?: string; last_seen_at?: string; archived?: boolean; snapshot?: HostSnapshot }
interface HostHistoryPoint { agent_id?: string; observed_at?: string; snapshot?: HostSnapshot }

const toHostMetrics = (agent: HostAgent, storageAvailable = true): HostMetrics => {
  const snapshot = agent.snapshot ?? {};
  const cpu = snapshot.cpu ?? {};
  const memory = snapshot.memory ?? {};
  const filesystems = snapshot.filesystems ?? [];
  const networks = snapshot.networks ?? [];
  return {
    host_id: agent.agent_id ?? '',
    host_name: agent.hostname ?? agent.agent_id ?? 'unknown',
    address: agent.hostname ?? agent.agent_id ?? '',
    status: agent.archived ? 'offline' : isFresh(agent.last_seen_at) ? 'online' : 'offline',
    timestamp: agent.last_seen_at ?? '',
    cpu: { usage: cpu.usage_percent ?? 0, usage_available: cpu.usage_available === true, cores: cpu.logical_cores ?? 0 },
    memory: { total: memory.total_bytes ?? 0, used: memory.used_bytes ?? 0, available: memory.available_bytes ?? 0, percent: memory.usage_percent ?? 0, percent_available: memory.usage_percent !== undefined },
    disks: filesystems.map((fs) => ({ device: fs.device ?? '', mountpoint: fs.mountpoint ?? '', total: fs.total_bytes ?? 0, used: fs.used_bytes ?? 0, percent: fs.usage_percent ?? 0, percent_available: fs.usage_percent !== undefined })),
    networks: networks.map((network) => ({ device: network.device ?? '', rx_speed: network.receive_bytes_per_second ?? 0, tx_speed: network.transmit_bytes_per_second ?? 0, rate_available: network.rate_available === true })),
    load: { load1: 0, load5: 0, load15: 0 },
    storage_available: storageAvailable
  };
};

const toHistoryPoint = (point: HostHistoryPoint): HistoryPoint => {
  const metric = toHostMetrics({ agent_id: point.agent_id, snapshot: point.snapshot, last_seen_at: point.observed_at });
  return { timestamp: point.observed_at ?? '', cpu_usage: metric.cpu.usage, cpu_available: metric.cpu.usage_available, memory_percent: metric.memory.percent, memory_available: metric.memory.percent_available, disk_percent: metric.disks[0]?.percent ?? 0, disk_available: metric.disks[0]?.percent_available ?? false, network_rx_speed: metric.networks[0]?.rx_speed ?? 0, network_tx_speed: metric.networks[0]?.tx_speed ?? 0, network_available: metric.networks[0]?.rate_available ?? false };
};

const isFresh = (value?: string) => !!value && Date.now() - Date.parse(value) < 60_000;
const durationMilliseconds = (value: string) => { const match = /^(\d+)([hdm])$/.exec(value); if (!match) return 60 * 60 * 1000; const amount = Number(match[1]); return amount * ({ m: 60_000, h: 3_600_000, d: 86_400_000 } as Record<string, number>)[match[2]]; };

/**
 * 格式化字节数为可读格式
 */
export const formatBytes = (bytes: number): string => {
  if (bytes === 0) return '0 B';
  const k = 1024;
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return Math.round(bytes / Math.pow(k, i) * 100) / 100 + ' ' + sizes[i];
};

/**
 * 格式化字节/秒为可读格式
 */
export const formatBytesPerSecond = (bytesPerSecond: number): string => {
  return formatBytes(bytesPerSecond) + '/s';
};
