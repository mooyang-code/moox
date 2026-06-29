import { callControl } from './http';
import type { PageReq, PageResult, ServiceDeployment, ServiceDeploymentWarning } from './types';

export interface ListServiceDeploymentsReq {
  service_name?: string;
  service_kind?: string;
  scope?: string;
  status?: string;
  page?: PageReq;
}

export interface ListServiceDeploymentsRsp {
  deployments: ServiceDeployment[];
  page_result?: PageResult;
  warnings?: ServiceDeploymentWarning[];
}

export function listServiceDeployments(req: ListServiceDeploymentsReq = {}) {
  return callControl<ListServiceDeploymentsReq, ListServiceDeploymentsRsp>('sysdeploy', 'ListServiceDeployments', req);
}

export function createServiceDeployment(deployment: ServiceDeployment) {
  return callControl<{ deployment: ServiceDeployment }, { deployment: ServiceDeployment; warnings?: ServiceDeploymentWarning[] }>(
    'sysdeploy',
    'CreateServiceDeployment',
    { deployment },
  );
}

export function updateServiceDeployment(serviceName: string, deployment: ServiceDeployment) {
  return callControl<{ service_name: string; deployment: ServiceDeployment }, { deployment: ServiceDeployment; warnings?: ServiceDeploymentWarning[] }>(
    'sysdeploy',
    'UpdateServiceDeployment',
    { service_name: serviceName, deployment },
  );
}

export function deleteServiceDeployment(serviceName: string) {
  return callControl<{ service_name: string }, { warnings?: ServiceDeploymentWarning[] }>('sysdeploy', 'DeleteServiceDeployment', { service_name: serviceName });
}
