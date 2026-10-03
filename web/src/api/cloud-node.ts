import { callControl, ControlRequestError } from "@/api/admin/http";
export { withOptionalSpace } from "@/api/space-context";

export interface CloudNode {
  id?: number;
  space_id?: string;
  node_id: string;
  node_type: string;
  trigger_type?: string;
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

export interface SCFFunctionRef {
  region: string;
  namespace: string;
  function_name: string;
}

export interface SCFFunctionCandidate {
  function: SCFFunctionRef;
  status: string;
  runtime: string;
  function_type: string;
  package_id: string;
  node_id: string;
  trigger_type: string;
  biz_type: string;
  import_state: string | number;
  importable: boolean;
  reason: string;
}

export interface SCFRegionScanError {
  region: string;
  message: string;
}

export interface SCFPreviewResponse {
  functions: SCFFunctionCandidate[];
  region_errors: SCFRegionScanError[];
}

export interface SCFFunctionImportResult {
  function: SCFFunctionRef;
  node_id: string;
  action: string;
  error_message: string;
}

export interface SCFImportResponse {
  results: SCFFunctionImportResult[];
  created: number;
  restored: number;
  unchanged: number;
  failed: number;
}

// SCF request-response calls may use the full 900s function timeout. Keep
// the browser-side deadline aligned with the Admin/CloudNode transport budget.
const scfSyncRequestConfig = { timeout: 960000 };

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
  trigger_type?: string;
  biz_type?: string;
  tag?: string;
  keyword?: string;
  page?: Page | number;
  page_size?: number;
}

export interface BatchCreateNodesRequest {
  nodes?: BatchCreateNodeItem[];
  cloud_account_id: string;
  region: string;
  namespace: string;
  node_type?: string;
  trigger_type?: string;
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
  trigger_type?: string;
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
  space_id: string;
  node_ids: string[];
  node_snapshots: CloudNodeDeleteSnapshot[];
}

export interface CloudNodeDeleteSnapshot {
  node_id: string;
  package_id?: string;
  deployment_id?: string;
  modify_time?: string;
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

function normalizePageParams<T extends { page?: Page | number; page_size?: number }>(params: T): T & { page?: Page } {
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

export const submitCreateNodes = async (data: BatchCreateNodesRequest): Promise<SubmitNodeBatchResponse> => {
  const nodes =
    data.nodes ??
    Array.from({ length: data.count }).map((_, index) => ({
      cloud_account_id: data.cloud_account_id,
      node_type: data.node_type,
      trigger_type: data.trigger_type,
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

export const batchDeleteNodes = async (
  data: BatchDeleteNodesRequest,
  options: { signal?: AbortSignal } = {}
): Promise<BatchDeleteNodesResponse> => {
  const spaceId = data.space_id.trim();
  if (!spaceId) throw new Error("删除采集云节点时必须指定空间");
  const snapshots = new Map(data.node_snapshots.map(snapshot => [snapshot.node_id, snapshot]));
  if (
    data.node_ids.length === 0 ||
    snapshots.size !== data.node_ids.length ||
    data.node_ids.some(nodeId => !snapshots.has(nodeId))
  ) {
    throw new Error("删除目标快照不完整，请刷新后重新选择");
  }
  const signal = options.signal;
  signal?.throwIfAborted();
  const lease = await callControl<{ space_id: string; holder_id: string }, { lease_id: string; fencing_token: string | number }>(
    "publishlease",
    "AcquireCollectorPublishLease",
    {
      space_id: spaceId,
      holder_id: crypto.randomUUID()
    }
  );
  const fence = { space_id: spaceId, lease_id: lease.lease_id, fencing_token: String(lease.fencing_token) };
  const pending = new Set<string>();
  let unknownSubmission = false;
  let submissionError: unknown;
  let leaseError: Error | undefined;
  let processedCount = 0;
  let failedCount = 0;
  let pollError: Error | undefined;
  const consecutivePollFailures = new Map<string, number>();
  const unresolvedPollErrors = new Map<string, string>();
  let renewAt = Date.now() + 30_000;
  let renewal: Promise<void> | undefined;
  // Renewal must continue even while a submission or status request is slow.
  const renewTimer = setInterval(() => {
    if (renewal || leaseError || signal?.aborted || Date.now() < renewAt) return;
    renewal = callControl<typeof fence, { lease_id: string; fencing_token: string | number }>(
      "publishlease",
      "RenewCollectorPublishLease",
      fence
    )
      .then(renewed => {
        if (renewed.lease_id !== lease.lease_id || String(renewed.fencing_token) !== fence.fencing_token) {
          leaseError = new Error("采集发布租约身份已变化，停止提交删除批次");
        }
        renewAt = Date.now() + 30_000;
      })
      .catch(error => {
        leaseError = new Error(
          `采集发布租约续租失败，停止提交新删除批次：${error instanceof Error ? error.message : String(error)}`
        );
      })
      .finally(() => {
        renewal = undefined;
      });
  }, 1_000);
  try {
    for (const snapshot of data.node_snapshots) {
      signal?.throwIfAborted();
      await assertCurrentDeleteSnapshot(spaceId, snapshot, signal);
    }
    for (let offset = 0; offset < data.node_ids.length; offset += 100) {
      signal?.throwIfAborted();
      if (leaseError) break;
      try {
        // Mark unknown before awaiting: an abort or a lost response can hide acceptance.
        unknownSubmission = true;
        const submitted = await waitWithAbort(
          callControl<
            { node_ids: string[]; collector_publish_lease_id: string; collector_publish_fencing_token: string },
            SubmitNodeBatchResponse
          >("cloudnode", "SubmitDeleteNodes", {
            node_ids: data.node_ids.slice(offset, offset + 100),
            collector_publish_lease_id: lease.lease_id,
            collector_publish_fencing_token: String(lease.fencing_token)
          }),
          signal
        );
        if (!submitted.job_id) throw new Error("删除批次响应缺少 job_id，请在云节点批次中查看结果");
        pending.add(submitted.job_id);
        unknownSubmission = false;
      } catch (error) {
        submissionError = error;
        break;
      }
    }
    while (pending.size > 0) {
      signal?.throwIfAborted();
      for (const jobId of pending) {
        if (unresolvedPollErrors.has(jobId)) continue;
        try {
          const status = await waitWithAbort(getNodeBatchChange(jobId), signal);
          consecutivePollFailures.delete(jobId);
          if (
            status.job.status === "NODE_BATCH_STATUS_SUCCESS" ||
            status.job.status === "NODE_BATCH_STATUS_FAILED" ||
            status.job.status === "NODE_BATCH_STATUS_PARTIAL"
          ) {
            pending.delete(jobId);
            processedCount += status.job.success_count ?? 0;
            failedCount += status.job.failed_count ?? 0;
            if (status.job.status !== "NODE_BATCH_STATUS_SUCCESS" && !submissionError) {
              submissionError = new Error("删除云函数失败");
            }
          }
        } catch (error) {
          signal?.throwIfAborted();
          const failures = (consecutivePollFailures.get(jobId) ?? 0) + 1;
          consecutivePollFailures.set(jobId, failures);
          const maxFailures = error instanceof ControlRequestError ? 3 : 5;
          if (failures >= maxFailures) {
            unresolvedPollErrors.set(jobId, error instanceof Error ? error.message : String(error));
            continue;
          }
          const delay = Math.min(250 * 2 ** (failures - 1), 2_000);
          await waitWithAbort(new Promise(resolve => setTimeout(resolve, delay)), signal);
        }
      }
      if (unresolvedPollErrors.size > 0 && pending.size === unresolvedPollErrors.size) {
        pollError = new Error(`删除批次状态无法确认，租约已保留：${[...unresolvedPollErrors.keys()].join(", ")}`);
        break;
      }
      if (pending.size > 0) await waitWithAbort(new Promise(resolve => setTimeout(resolve, 250)), signal);
    }
    if (failedCount > 0) throw new Error(`删除云函数失败：${failedCount} 个节点未完成`);
    if (submissionError) throw submissionError;
    if (pollError) throw pollError;
    if (leaseError) throw leaseError;
    return { processed_count: processedCount };
  } finally {
    clearInterval(renewTimer);
    if (renewal && !signal?.aborted) await renewal;
    if (!unknownSubmission && pending.size === 0) {
      await callControl<{ space_id: string; lease_id: string; fencing_token: string }, { released: boolean }>(
        "publishlease",
        "ReleaseCollectorPublishLease",
        fence
      );
    }
  }
};

async function assertCurrentDeleteSnapshot(
  spaceId: string,
  expected: CloudNodeDeleteSnapshot,
  signal?: AbortSignal
): Promise<void> {
  const pageSize = 100;
  for (let page = 1; ; page++) {
    const current = await waitWithAbort(getNodeList({ node_id: expected.node_id, page, page_size: pageSize }), signal);
    const match = current.items.find(item => item.node_id === expected.node_id);
    if (match) {
      const sameIdentity =
        (match.space_id ?? spaceId) === spaceId &&
        (match.package_id ?? "") === (expected.package_id ?? "") &&
        (match.deployment_id ?? "") === (expected.deployment_id ?? "") &&
        (match.modify_time ?? "") === (expected.modify_time ?? "");
      if (sameIdentity) return;
      throw new Error(`节点 ${expected.node_id} 已发生变化，请刷新后重新选择删除目标`);
    }
    if (!current.page?.has_more) {
      throw new Error(`节点 ${expected.node_id} 已不存在，请刷新后重新选择删除目标`);
    }
  }
}

function waitWithAbort<T>(operation: Promise<T>, signal?: AbortSignal): Promise<T> {
  if (!signal) return operation;
  return new Promise<T>((resolve, reject) => {
    const abort = () => reject(signal.reason ?? new DOMException("Aborted", "AbortError"));
    signal.addEventListener("abort", abort, { once: true });
    operation.then(resolve, reject).finally(() => signal.removeEventListener("abort", abort));
    if (signal.aborted) abort();
  });
}

export const listCloudRegions = async (provider = "tencent"): Promise<CloudRegion[]> => {
  const rsp = await callControl<{ provider?: string }, { regions?: CloudRegion[] }>(
    "cloudnode",
    "ListCloudRegions",
    provider ? { provider } : {}
  );
  return rsp.regions ?? [];
};

export const previewSCFFunctions = async (accountId: string): Promise<SCFPreviewResponse> => {
  const rsp = await callControl<{ account_id: string }, SCFPreviewResponse>(
    "cloudnode",
    "PreviewSCFFunctions",
    {
      account_id: accountId
    },
    scfSyncRequestConfig
  );
  return { functions: rsp.functions ?? [], region_errors: rsp.region_errors ?? [] };
};

export const importSCFFunctions = async (accountId: string, functions: SCFFunctionRef[]): Promise<SCFImportResponse> => {
  const rsp = await callControl<{ account_id: string; functions: SCFFunctionRef[] }, SCFImportResponse>(
    "cloudnode",
    "ImportSCFFunctions",
    { account_id: accountId, functions },
    scfSyncRequestConfig
  );
  return {
    results: rsp.results ?? [],
    created: Number(rsp.created ?? 0),
    restored: Number(rsp.restored ?? 0),
    unchanged: Number(rsp.unchanged ?? 0),
    failed: Number(rsp.failed ?? 0)
  };
};
