import { describe, expect, it } from 'vitest';
import type { ServiceDeployment } from '@/api/admin/types';
import { serviceDeploymentRowKey } from './health';

describe('service deployment health helpers', () => {
  it('uses node and service name as the stable row identity', () => {
    expect(serviceDeploymentRowKey({ node_id: 'gateway-hk-177', service_name: 'monitor' } as ServiceDeployment)).toBe(
      'gateway-hk-177:monitor',
    );
  });
});
