export interface FilesystemUsageValue {
  percent: number;
  percent_available: boolean;
}

export interface NetworkRateValue {
  rx_speed: number;
  tx_speed: number;
  rate_available: boolean;
}

export interface HistoryAvailabilityValue {
  cpu_available: boolean;
  memory_available: boolean;
  disk_available: boolean;
}

export const maxAvailableFilesystemUsage = (items: FilesystemUsageValue[]): number | null => {
  const values = items.filter((item) => item.percent_available).map((item) => item.percent);
  return values.length ? Math.max(...values) : null;
};

export const aggregateNetworkRate = (items: NetworkRateValue[]): { rx: number; tx: number } | null => {
  const available = items.filter((item) => item.rate_available);
  if (!available.length) return null;
  return available.reduce(
    (sum, item) => ({ rx: sum.rx + item.rx_speed, tx: sum.tx + item.tx_speed }),
    { rx: 0, tx: 0 },
  );
};

export const memoryUsageAvailable = (totalBytes: number | undefined, metricPresent: boolean): boolean => metricPresent && (totalBytes ?? 0) > 0;

export const filesystemUsageAvailable = (device: string | undefined, mountpoint: string | undefined, totalBytes: number | undefined): boolean => Boolean(device || mountpoint || (totalBytes ?? 0) > 0);

export const metricValueAvailable = (status: string, available: boolean): boolean => status === 'online' && available;

export const historyHasRenderableData = (points: HistoryAvailabilityValue[]): boolean => points.some(
  (point) => point.cpu_available || point.memory_available || point.disk_available,
);
