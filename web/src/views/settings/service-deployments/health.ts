import type { ServiceDeployment, ServiceDeploymentInput } from '@/api/admin/types';
import type { CheckResult, MonitorCheck } from '@/api/monitor';

export type DeploymentHealthState = 'healthy' | 'unhealthy' | 'unknown';

export function sysDeployChecksRequest() {
  return { source: 'sysdeploy', page: { page: 1, size: 500 } };
}

export function deploymentAccessAddress(deployment: ServiceDeployment) {
  return deployment.base_url || deployment.rpc_address || `${deployment.host}:${deployment.port}`;
}

export function deploymentHealthState(result?: CheckResult): DeploymentHealthState {
  if (!result) return 'unknown';
  return result.success ? 'healthy' : 'unhealthy';
}

export function serviceDeploymentRowKey(deployment: Pick<ServiceDeployment, 'node_id' | 'service_name'>) {
  return `${deployment.node_id}:${deployment.service_name}`;
}

export function validateGatewayDeployment(deployment: Pick<ServiceDeploymentInput, 'gateway_enabled' | 'host' | 'gateway_path' | 'gateway_service_id'>) {
  if (!deployment.gateway_enabled) return '';
  if (deployment.host !== '127.0.0.1' && deployment.host !== '::1') return '网关暴露的服务 Host 只能是 127.0.0.1 或 ::1';
  if (!deployment.gateway_service_id.trim()) return '请填写 Gateway service ID';
  const servicePath = deployment.gateway_path?.trim() || '';
  if (!servicePath || !servicePath.startsWith('trpc.')) return '请填写有效的 tRPC service path';
  return '';
}

export function gatewayNodeOnlineState(lastSeenAt?: string, now = new Date()) {
  if (!lastSeenAt) return { state: 'never' as const, label: '未上报' };
  const timestamp = new Date(lastSeenAt).getTime();
  if (!Number.isFinite(timestamp)) return { state: 'never' as const, label: '未上报' };
  const online = now.getTime() - timestamp <= 2 * 60 * 1000;
  return online ? { state: 'online' as const, label: '在线' } : { state: 'offline' as const, label: '离线' };
}

export function gatewayHashState(expected?: string, applied?: string) {
  if (!expected || expected !== applied) return { state: 'mismatch' as const, label: '待同步' };
  return { state: 'synced' as const, label: '已同步' };
}

export async function loadLatestHealthResults(
  checks: MonitorCheck[],
  fetchLatest: (check: MonitorCheck) => Promise<CheckResult | undefined>,
) {
  const settled = await Promise.allSettled(
    checks.map(async (check) => {
      if (!check.check_id) return undefined;
      const result = await fetchLatest(check);
      return result ? ([check.check_id, result] as const) : undefined;
    }),
  );
  const results: Record<string, CheckResult> = {};
  settled.forEach((item) => {
    if (item.status === 'fulfilled' && item.value) results[item.value[0]] = item.value[1];
  });
  return results;
}
