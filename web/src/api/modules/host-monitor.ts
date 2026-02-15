import { api } from '@/api/config';

// ========== 类型定义 ==========

// 主机监控指标
export interface HostMetrics {
  host_id: number;
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
}

export interface CPUMetrics {
  usage: number;  // CPU使用率（百分比）
  cores: number;  // CPU核心数
}

export interface MemoryMetrics {
  total: number;     // 总内存（字节）
  used: number;      // 已用内存（字节）
  available: number; // 可用内存（字节）
  percent: number;   // 使用率（百分比）
}

export interface DiskMetrics {
  device: string;
  mountpoint: string;
  total: number;   // 总容量（字节）
  used: number;    // 已用容量（字节）
  percent: number; // 使用率（百分比）
}

export interface NetworkSpeed {
  device: string;
  rx_speed: number; // 接收速率（字节/秒）
  tx_speed: number; // 发送速率（字节/秒）
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
  memory_percent: number;
  disk_percent: number;
  network_rx_speed: number;
  network_tx_speed: number;
}

// 测试结果
export interface TestResult {
  reachable: boolean;
  message: string;
  duration_ms?: number;
  metrics_count?: number;
}

// ========== API接口 ==========

/**
 * 启用主机监控
 * @param hostId 主机ID
 */
export const enableMonitor = (hostId: number) => {
  return api.post('/monitor/EnableMonitor', {
    host_id: hostId
  });
};

/**
 * 禁用主机监控
 * @param hostId 主机ID
 */
export const disableMonitor = (hostId: number) => {
  return api.post('/monitor/DisableMonitor', {
    host_id: hostId
  });
};

/**
 * 获取主机监控状态
 * @param hostId 主机ID
 */
export const getMonitorStatus = (hostId: number) => {
  return api.post('/monitor/GetMonitorStatus', {
    host_id: hostId
  });
};

/**
 * 获取当前监控指标
 * @param hostIds 主机ID列表（可选，不传则返回所有启用监控的主机）
 */
export const getCurrentMetrics = (hostIds?: number[]) => {
  const params: any = {};
  if (hostIds && hostIds.length > 0) {
    params.host_ids = hostIds.join(',');
  }
  return api.post('/monitor/GetCurrentMetrics', params);
};

/**
 * 获取历史监控数据
 * @param hostAddress 主机IP地址
 * @param duration 时间范围（如 "1h", "24h", "7d"）
 */
export const getHistoryMetrics = (hostAddress: string, duration: string = '1h') => {
  return api.post('/monitor/GetHistoryMetrics', {
    host_address: hostAddress,
    duration: duration
  });
};

/**
 * 测试 Node Exporter 连通性
 * @param hostId 主机ID
 */
export const testNodeExporter = (hostId: number) => {
  return api.post('/monitor/TestNodeExporter', {
    host_id: hostId
  });
};

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
