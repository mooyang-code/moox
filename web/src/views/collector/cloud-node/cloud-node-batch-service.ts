import { batchCreateNodes, batchDeleteNodes, batchDeployNodes } from "@/api/cloud-node";
import { BatchChangeStatus } from "@/utils/cloud-node-batch-change";
import type { BatchChangeStatusResponse } from "@/utils/cloud-node-batch-change";
import { parseMetadata } from "./cloud-node-model";

export interface CloudNodeBatchChange {
  batchChangeType: string;
  requestPayload: Record<string, any>;
}

export function makeCompletedBatchChangeStatus(
  batchId: string,
  batchChangeType: string,
  total: number
): BatchChangeStatusResponse {
  const now = new Date().toISOString();
  return {
    batch_id: batchId,
    batch_change_type: batchChangeType,
    batch_change_status: BatchChangeStatus.SUCCESS,
    total_count: total,
    success_count: total,
    failed_count: 0,
    progress: 100,
    created_at: now,
    completed_time: now,
    failed_items: []
  };
}

export async function submitCloudNodeBatchChange(batchChanges: CloudNodeBatchChange[]) {
  if (batchChanges.length === 0) throw new Error("没有可提交的云节点批量变更");
  const batchChangeType = batchChanges[0].batchChangeType;
  const first = batchChanges[0].requestPayload || {};

  if (batchChangeType === "CREATE_NODE") {
    const nodes = batchChanges.map((batchChange, index) => {
      const params = batchChange.requestPayload || {};
      const functionNamePrefix =
        params.function_name_prefix ||
        params.function_name ||
        first.function_name_prefix ||
        first.function_name ||
        "moox-cloudnode";
      return {
        cloud_account_id: params.cloud_account_id,
        node_type: params.node_type,
        region: params.region,
        namespace: params.namespace || first.namespace || "default",
        runtime: params.runtime || first.runtime || "Go1",
        handler: params.handler || first.handler || "main",
        package_id: params.package_id || first.package_id,
        config: params.config,
        environment: params.environment,
        metadata: {
          ...parseMetadata(params.metadata),
          biz_type: params.biz_type,
          tag: params.tag,
          function_name_prefix: functionNamePrefix,
          timeout_threshold: params.timeout_threshold,
          heartbeat_interval: params.heartbeat_interval,
          probe_enabled: params.probe_enabled,
          index
        }
      };
    });
    const rsp = await batchCreateNodes({
      nodes,
      cloud_account_id: first.cloud_account_id,
      region: first.region,
      namespace: first.namespace || "default",
      node_type: first.node_type,
      function_name_prefix: first.function_name_prefix || first.function_name || "moox-cloudnode",
      runtime: first.runtime || "Go1",
      handler: first.handler || "main",
      package_id: first.package_id,
      count: batchChanges.length,
      config: first.config,
      environment: first.environment
    });
    if (!rsp.batch_id) throw new Error("cloudnode 未返回 batch_id");
    return rsp.batch_id;
  }

  if (batchChangeType === "DELETE_NODE") {
    const rsp = await batchDeleteNodes({ node_ids: batchChanges.map(item => item.requestPayload?.node_id).filter(Boolean) });
    if (!rsp.batch_id) throw new Error("cloudnode 未返回 batch_id");
    return rsp.batch_id;
  }

  if (batchChangeType === "DEPLOY_NODE") {
    const rsp = await batchDeployNodes({
      node_ids: batchChanges.map(item => item.requestPayload?.node_id).filter(Boolean),
      package_id: first.package_id
    });
    if (!rsp.batch_id) throw new Error("cloudnode 未返回 batch_id");
    return rsp.batch_id;
  }

  throw new Error(`unsupported cloud node batch change type: ${batchChangeType}`);
}
