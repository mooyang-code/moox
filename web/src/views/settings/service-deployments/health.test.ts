import { describe, expect, it } from 'vitest';
import type { ServiceDeployment } from '@/api/admin/types';
import type { CheckResult, MonitorCheck } from '@/api/monitor';
import { deploymentAccessAddress, deploymentHealthState, loadLatestHealthResults, serviceDeploymentRowKey, sysDeployChecksRequest } from './health';

describe('service deployment health helpers', () => {
  it('prefers the protocol-specific derived access address', () => {
    const http = { protocol: 'http', host: '127.0.0.1', port: 11000, base_url: 'http://127.0.0.1:11000' } as ServiceDeployment;
    const trpc = { protocol: 'trpc', host: '127.0.0.1', port: 20100, rpc_address: '127.0.0.1:20100' } as ServiceDeployment;

    expect(deploymentAccessAddress(http)).toBe('http://127.0.0.1:11000');
    expect(deploymentAccessAddress(trpc)).toBe('127.0.0.1:20100');
  });

  it('keeps desired state separate from observed health', () => {
    expect(deploymentHealthState(undefined)).toBe('unknown');
    expect(deploymentHealthState({ success: true })).toBe('healthy');
    expect(deploymentHealthState({ success: false })).toBe('unhealthy');
  });

  it('keeps other health results when one monitor request fails', async () => {
    const checks: MonitorCheck[] = [
      { check_id: 'service-a', space_id: 'moox-system' },
      { check_id: 'service-b', space_id: 'moox-system' },
    ];
    const result = await loadLatestHealthResults(checks, async (check) => {
      if (check.check_id === 'service-b') throw new Error('monitor unavailable');
      return { success: true, checked_at: '2026-07-13T00:00:00Z' } as CheckResult;
    });

    expect(result['service-a']?.success).toBe(true);
    expect(result['service-b']).toBeUndefined();
  });

  it('queries Monitor in the default space used by SysDeploy sync', () => {
    expect(sysDeployChecksRequest()).toEqual({ source: 'sysdeploy', page: { page: 1, size: 500 } });
  });

  it('uses node and service name as the stable row identity', () => {
    expect(serviceDeploymentRowKey({ node_id: 'gateway-hk-177', service_name: 'monitor' } as ServiceDeployment)).toBe(
      'gateway-hk-177:monitor',
    );
  });
});
