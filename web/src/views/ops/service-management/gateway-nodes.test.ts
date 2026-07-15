import fs from 'node:fs';
import path from 'node:path';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { callControl } from '@/api/admin/http';
import {
  deleteGatewayNode,
  deleteServiceDeployment,
  getGatewayNodeRoutes,
  listGatewayNodes,
  listServiceDeployments,
  updateServiceDeployment,
} from '@/api/admin/sysdeploy';
import type { ServiceDeploymentInput } from '@/api/admin/types';
import {
  gatewayHashState,
  gatewayNodeOnlineState,
  serviceDeploymentRowKey,
  validateGatewayDeployment,
} from '@/views/settings/service-deployments/health';

vi.mock('@/api/admin/http', () => ({ callControl: vi.fn() }));

const mockedCallControl = vi.mocked(callControl);

describe('gateway node and service instance contracts', () => {
  beforeEach(() => mockedCallControl.mockReset());

  it('keeps the four service management tabs in the required order', () => {
    const source = fs.readFileSync(path.resolve(__dirname, 'index.vue'), 'utf8');
    const positions = ['网关节点', '服务实例', '可用性监控', '应用指标'].map((label) => source.indexOf(`label: '${label}'`));
    expect(positions.every((position) => position >= 0)).toBe(true);
    expect(positions).toEqual([...positions].sort((left, right) => left - right));
  });

  it('labels heartbeat and route hash state without relying on color alone', () => {
    expect(gatewayNodeOnlineState('2026-07-15T10:00:00Z', new Date('2026-07-15T10:01:00Z'))).toEqual({
      state: 'online',
      label: '在线',
    });
    expect(gatewayNodeOnlineState('', new Date('2026-07-15T10:01:00Z')).label).toBe('未上报');
    expect(gatewayHashState('expected', 'applied')).toEqual({ state: 'mismatch', label: '待同步' });
    expect(gatewayHashState('same', 'same')).toEqual({ state: 'synced', label: '已同步' });
  });

  it('propagates node filters and composite identities to deployment operations', async () => {
    mockedCallControl.mockResolvedValue({ deployments: [] });
    await listServiceDeployments({ node_id: 'gateway-gz-122', gateway_enabled: true });
    expect(mockedCallControl).toHaveBeenLastCalledWith('sysdeploy', 'ListServiceDeployments', {
      node_id: 'gateway-gz-122',
      gateway_enabled: true,
    });

    const deployment = { node_id: 'gateway-gz-122', service_name: 'monitor' } as ServiceDeploymentInput;
    await updateServiceDeployment('gateway-gz-122', 'monitor', deployment);
    expect(mockedCallControl).toHaveBeenLastCalledWith('sysdeploy', 'UpdateServiceDeployment', {
      node_id: 'gateway-gz-122',
      service_name: 'monitor',
      deployment,
    });
    await deleteServiceDeployment('gateway-gz-122', 'monitor');
    expect(mockedCallControl).toHaveBeenLastCalledWith('sysdeploy', 'DeleteServiceDeployment', {
      node_id: 'gateway-gz-122',
      service_name: 'monitor',
    });
    expect(serviceDeploymentRowKey(deployment)).toBe('gateway-gz-122:monitor');
  });

  it('supports gateway node listing, route inspection, and deletion APIs', async () => {
    mockedCallControl.mockResolvedValue({ nodes: [] });
    await listGatewayNodes({ node_id: 'gateway-gz-122', status: 'enabled' });
    expect(mockedCallControl).toHaveBeenLastCalledWith('sysdeploy', 'ListGatewayNodes', {
      node_id: 'gateway-gz-122', status: 'enabled',
    });
    await getGatewayNodeRoutes('gateway-gz-122');
    expect(mockedCallControl).toHaveBeenLastCalledWith('sysdeploy', 'GetGatewayNodeRoutes', { node_id: 'gateway-gz-122' });
    await deleteGatewayNode('gateway-gz-122');
    expect(mockedCallControl).toHaveBeenLastCalledWith('sysdeploy', 'DeleteGatewayNode', { node_id: 'gateway-gz-122' });
  });

  it('requires loopback host and a tRPC path only when gateway exposure is enabled', () => {
    const valid = {
      gateway_enabled: true,
      host: '127.0.0.1',
      gateway_path: 'trpc.moox.monitor.Monitor',
      gateway_service_id: 'monitor',
    } as ServiceDeploymentInput;
    expect(validateGatewayDeployment(valid)).toBe('');
    expect(validateGatewayDeployment({ ...valid, host: '10.0.0.8' })).toContain('127.0.0.1');
    expect(validateGatewayDeployment({ ...valid, host: '::1' })).toBe('');
    expect(validateGatewayDeployment({ ...valid, gateway_path: '' })).toContain('tRPC');
    expect(validateGatewayDeployment({ ...valid, gateway_service_id: '' })).toContain('service ID');
    expect(validateGatewayDeployment({ ...valid, gateway_enabled: false, host: '10.0.0.8', gateway_path: '' })).toBe('');
  });
});
