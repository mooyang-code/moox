import { callControl } from "@/api/admin/http";
export { withOptionalSpace } from "@/api/space-context";

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
  biz_type?: string;
  tag?: string;
  ip_address?: string;
  metadata?: Record<string, unknown>;
  timeout_threshold?: number;
  probe_enabled?: boolean;
  probe_url?: string;
  is_deleted?: boolean;
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
  keyword?: string;
  page?: Page | number;
  page_size?: number;
}

export interface UpdateNodeRequest {
  node_id: string;
  namespace?: string;
  region?: string;
  package_id?: string;
  package_version?: string;
  deployment_id?: string;
  timeout_threshold?: number;
  probe_enabled?: boolean;
  probe_url?: string;
  metadata?: Record<string, unknown> | string;
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

export type NodeBatchStatus =
  | "NODE_BATCH_STATUS_PENDING"
  | "NODE_BATCH_STATUS_RUNNING"
  | "NODE_BATCH_STATUS_SUCCESS"
  | "NODE_BATCH_STATUS_FAILED"
  | "NODE_BATCH_STATUS_PARTIAL";

export type NodeBatchItemStatus =
  | "NODE_BATCH_ITEM_STATUS_PENDING"
  | "NODE_BATCH_ITEM_STATUS_RUNNING"
  | "NODE_BATCH_ITEM_STATUS_SUCCESS"
  | "NODE_BATCH_ITEM_STATUS_FAILED";

export interface SubmitNodeBatchResponse {
  job_id: string;
  operation: string;
  total_count: number;
}

export interface NodeBatchSummary extends SubmitNodeBatchResponse {
  status: NodeBatchStatus;
  pending_count: number;
  running_count: number;
  success_count: number;
  failed_count: number;
  progress_percent: number;
  created_at: string;
  completed_at?: string;
}

export interface NodeBatchItemResult {
  item_id: string;
  node_id: string;
  status: NodeBatchItemStatus;
  result_summary?: string;
  error_message?: string;
  started_at?: string;
  completed_at?: string;
}

export interface GetNodeBatchChangeResponse {
  job: NodeBatchSummary;
  items: NodeBatchItemResult[];
}

export interface BatchDeleteNodesResponse {
  processed_count: number;
}

function normalizePageParams<T extends { page?: Page | number; page_size?: number }>(
  params: T
): T & { page?: Page } {
  const normalized = { ...params } as T & { page?: Page };
  if (typeof params.page === "number" || params.page_size !== undefined) {
    normalized.page = {
      page: typeof params.page === "number" ? params.page : params.page?.page,
      size: params.page_size ?? (typeof params.page === "object" ? params.page?.size : undefined)
    };
  }
  delete (normalized as { page_size?: number }).page_size;
  return normalized;
}

export const getNodeList = async (
  params: GetNodeListRequest = {}
): Promise<{ items: CloudNode[]; total: number; page?: PageResult }> => {
  const rsp = await callControl<GetNodeListRequest, { items?: CloudNode[]; page?: PageResult }>(
    "cloudnode",
    "GetNodeList",
    normalizePageParams(params)
  );
  return { items: rsp.items ?? [], total: rsp.page?.total ?? 0, page: rsp.page };
};

export const updateNode = async (data: UpdateNodeRequest): Promise<void> => {
  await callControl<{ node: UpdateNodeRequest }, Record<string, never>>("cloudnode", "UpdateNode", { node: data });
};

export const submitCreateNodes = async (data: BatchCreateNodesRequest): Promise<SubmitNodeBatchResponse> => {
  const nodes =
    data.nodes ??
    Array.from({ length: data.count }).map((_, index) => ({
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
  return callControl<{ nodes: BatchCreateNodeItem[] }, SubmitNodeBatchResponse>("cloudnode", "SubmitCreateNodes", { nodes });
};

export const submitDeployNodes = async (data: BatchDeployNodesRequest): Promise<SubmitNodeBatchResponse> => {
  const deployments = data.node_ids.map(id => ({ node_id: id, package_id: data.package_id }));
  return callControl<{ deployments: Array<{ node_id: string; package_id: string }> }, SubmitNodeBatchResponse>(
    "cloudnode",
    "SubmitDeployNodes",
    { deployments }
  );
};

export const getNodeBatchChange = async (jobId: string): Promise<GetNodeBatchChangeResponse> =>
  callControl<{ job_id: string }, GetNodeBatchChangeResponse>("cloudnode", "GetNodeBatchChange", { job_id: jobId });

export const batchDeleteNodes = async (data: BatchDeleteNodesRequest): Promise<BatchDeleteNodesResponse> => {
  const rsp = await callControl<{ node_ids: string[] }, Partial<BatchDeleteNodesResponse>>("cloudnode", "BatchDeleteNodes", {
    node_ids: data.node_ids
  });
  return { processed_count: rsp.processed_count ?? 0 };
};

export const listCloudRegions = async (provider = "tencent"): Promise<CloudRegion[]> => {
  const rsp = await callControl<{ provider?: string }, { regions?: CloudRegion[] }>(
    "cloudnode",
    "ListCloudRegions",
    provider ? { provider } : {}
  );
  return rsp.regions ?? [];
};
