import { callControl } from '@/api/admin/http';
export { withOptionalSpace } from '@/api/space-context';

export type NodeStatusCode = 'NODE_STATUS_UNSPECIFIED' | 'NODE_STATUS_OFFLINE' | 'NODE_STATUS_ONLINE' | 'NODE_STATUS_TIMEOUT' | 'NODE_STATUS_ABNORMAL';

export interface CloudNode {
  id?: number;
  space_id?: string;
  node_id: string;
  node_type: string;
  cloud_account_id: string;
  region: string;
  namespace: string;
  provider?: string;
  function_name?: string;
  package_id?: string;
  package_version?: string;
  deployment_id?: string;
  running_version?: string;
  biz_type?: string;
  tag?: string;
  ip_address?: string;
  supported_workloads?: string[];
  metadata?: Record<string, unknown>;
  timeout_threshold?: number;
  heartbeat_interval?: number;
  probe_enabled?: boolean;
  probe_url?: string;
  status?: NodeStatusCode | number;
  last_heartbeat?: string;
  is_deleted?: boolean;
  cls_topic_id?: string;
  create_time?: string;
  modify_time?: string;
}

export interface CloudRegion {
  code: string;
  name: string;
  tag: string;
  max_nodes?: number;
  max_namespaces_per_region?: number;
  max_functions_per_namespace?: number;
}

export interface Page {
  page?: number;
  size?: number;
}

export interface PageResult {
  page?: number;
  size?: number;
  total?: number;
  has_more?: boolean;
}

export interface GetNodeListRequest {
  node_id?: string;
  cloud_account_id?: string;
  namespace?: string;
  region?: string;
  node_type?: string;
  biz_type?: string;
  tag?: string;
  status?: NodeStatusCode | number;
  keyword?: string;
  page?: Page;
}

export interface UpdateNodeRequest {
  node_id: string;
  namespace?: string;
  region?: string;
  package_id?: string;
  package_version?: string;
  deployment_id?: string;
  supported_workloads?: string[];
  metadata?: Record<string, unknown>;
}

export interface BatchCreateNodesRequest {
  nodes?: BatchCreateNodeItem[];
  cloud_account_id: string;
  region: string;
  namespace: string;
  node_type?: string;
  function_name_prefix: string;
  runtime: string;
  handler?: string;
  package_id?: string;
  deployment_id?: string;
  count: number;
  config?: Record<string, string>;
  environment?: Record<string, string>;
  metadata?: Record<string, unknown>;
}

export interface BatchCreateNodeItem {
  cloud_account_id: string;
  node_type?: string;
  runtime: string;
  handler?: string;
  config?: Record<string, string>;
  environment?: Record<string, string>;
  region: string;
  namespace?: string;
  package_id?: string;
  deployment_id?: string;
  metadata?: Record<string, unknown>;
}

export interface BatchDeployNodesRequest {
  node_ids: string[];
  package_id: string;
}

export interface BatchDeleteNodesRequest {
  node_ids: string[];
}

export interface BatchChangeResult {
  batch_id: string;
  processed_count: number;
}

export const NODE_STATUS_LABEL: Record<string, string> = {
  NODE_STATUS_ONLINE: '在线',
  NODE_STATUS_OFFLINE: '离线',
  NODE_STATUS_TIMEOUT: '超时',
  NODE_STATUS_ABNORMAL: '异常',
  NODE_STATUS_UNSPECIFIED: '未知'
};

export const getNodeList = async (params: GetNodeListRequest = {}): Promise<{ items: CloudNode[]; total: number; page?: PageResult }> => {
  const rsp = await callControl<GetNodeListRequest, { items?: CloudNode[]; page?: PageResult }>('cloudnode', 'GetNodeList', params);
  return { items: rsp.items ?? [], total: rsp.page?.total ?? 0, page: rsp.page };
};

export const updateNode = async (data: UpdateNodeRequest): Promise<void> => {
  await callControl<{ node: UpdateNodeRequest }, Record<string, never>>('cloudnode', 'UpdateNode', { node: data });
};

export const batchCreateNodes = async (data: BatchCreateNodesRequest): Promise<BatchChangeResult> => {
  const nodes = data.nodes ?? Array.from({ length: data.count }).map((_, index) => ({
    cloud_account_id: data.cloud_account_id,
    node_type: data.node_type,
    region: data.region,
    namespace: data.namespace,
    runtime: data.runtime,
    handler: data.handler,
    package_id: data.package_id,
    deployment_id: data.deployment_id,
    config: data.config,
    environment: data.environment,
    metadata: {
      ...(data.metadata ?? {}),
      function_name_prefix: data.function_name_prefix,
      index
    }
  }));
  const rsp = await callControl<{ nodes: BatchCreateNodeItem[] }, Partial<BatchChangeResult>>('cloudnode', 'BatchCreateNodes', { nodes });
  return { batch_id: rsp.batch_id ?? '', processed_count: rsp.processed_count ?? 0 };
};

export const batchDeployNodes = async (data: BatchDeployNodesRequest): Promise<BatchChangeResult> => {
  const deployments = data.node_ids.map(id => ({ node_id: id, package_id: data.package_id }));
  const rsp = await callControl<{ deployments: Array<{ node_id: string; package_id: string }> }, Partial<BatchChangeResult>>(
    'cloudnode',
    'BatchDeployNodes',
    { deployments }
  );
  return { batch_id: rsp.batch_id ?? '', processed_count: rsp.processed_count ?? 0 };
};

export const batchDeleteNodes = async (data: BatchDeleteNodesRequest): Promise<BatchChangeResult> => {
  const rsp = await callControl<{ node_ids: string[] }, Partial<BatchChangeResult>>('cloudnode', 'BatchDeleteNodes', { node_ids: data.node_ids });
  return { batch_id: rsp.batch_id ?? '', processed_count: rsp.processed_count ?? 0 };
};

export const listCloudRegions = async (provider = 'tencent'): Promise<CloudRegion[]> => {
  const rsp = await callControl<{ provider?: string }, { regions?: CloudRegion[] }>('cloudnode', 'ListCloudRegions', provider ? { provider } : {});
  return rsp.regions ?? [];
};
