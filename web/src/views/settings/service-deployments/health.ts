import type { ServiceDeployment } from '@/api/admin/types';
import type { CheckResult, MonitorCheck } from '@/api/monitor';

export type DeploymentHealthState = 'healthy' | 'unhealthy' | 'unknown';

export function deploymentAccessAddress(deployment: ServiceDeployment) {
  return deployment.base_url || deployment.rpc_address || `${deployment.host}:${deployment.port}`;
}

export function deploymentHealthState(result?: CheckResult): DeploymentHealthState {
  if (!result) return 'unknown';
  return result.success ? 'healthy' : 'unhealthy';
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
