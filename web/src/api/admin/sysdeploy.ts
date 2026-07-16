import { callControl } from './http';
import type {
  GatewayNode,
  GatewayNodeInput,
  GatewayRoute,
  PageReq,
  PageResult,
  ServiceDeployment,
  ServiceDeploymentInput,
  ServiceDeploymentWarning,
} from './types';

export interface ListServiceDeploymentsReq {
  service_name?: string;
  service_kind?: string;
  scope?: string;
  status?: string;
  node_id?: string;
  gateway_enabled?: boolean;
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

export function createServiceDeployment(deployment: ServiceDeploymentInput) {
  return callControl<{ deployment: ServiceDeploymentInput }, { deployment: ServiceDeployment; warnings?: ServiceDeploymentWarning[] }>(
    'sysdeploy',
    'CreateServiceDeployment',
    { deployment },
  );
}

export function updateServiceDeployment(nodeId: string, serviceName: string, deployment: ServiceDeploymentInput) {
  return callControl<{ node_id: string; service_name: string; deployment: ServiceDeploymentInput }, { deployment: ServiceDeployment; warnings?: ServiceDeploymentWarning[] }>(
    'sysdeploy',
    'UpdateServiceDeployment',
    { node_id: nodeId, service_name: serviceName, deployment },
  );
}

export function deleteServiceDeployment(nodeId: string, serviceName: string) {
  return callControl<{ node_id: string; service_name: string }, { warnings?: ServiceDeploymentWarning[] }>('sysdeploy', 'DeleteServiceDeployment', {
    node_id: nodeId,
    service_name: serviceName,
  });
}

export interface ListGatewayNodesReq {
  node_id?: string;
  status?: string;
  page?: PageReq;
}

export interface ListGatewayNodesRsp {
  nodes: GatewayNode[];
  page_result?: PageResult;
}

export function listGatewayNodes(req: ListGatewayNodesReq = {}) {
  return callControl<ListGatewayNodesReq, ListGatewayNodesRsp>('sysdeploy', 'ListGatewayNodes', req);
}

export function createGatewayNode(node: GatewayNodeInput) {
  return callControl<{ node: GatewayNodeInput }, { node: GatewayNode }>('sysdeploy', 'CreateGatewayNode', { node });
}

export function updateGatewayNode(nodeId: string, node: GatewayNodeInput) {
  return callControl<{ node_id: string; node: GatewayNodeInput }, { node: GatewayNode }>('sysdeploy', 'UpdateGatewayNode', {
    node_id: nodeId,
    node,
  });
}

export function deleteGatewayNode(nodeId: string) {
  return callControl<{ node_id: string }, Record<string, never>>('sysdeploy', 'DeleteGatewayNode', { node_id: nodeId });
}

export function getGatewayNodeRoutes(nodeId: string) {
  return callControl<{ node_id: string }, { node_id: string; route_hash: string; generated_at: string; disabled: boolean; routes: GatewayRoute[] }>(
    'sysdeploy',
    'GetGatewayNodeRoutes',
    { node_id: nodeId },
  );
}
