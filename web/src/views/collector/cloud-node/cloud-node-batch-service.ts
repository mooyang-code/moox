import { submitCreateNodes, submitDeployNodes } from "@/api/cloud-node";
import type { SubmitNodeBatchResponse } from "@/api/cloud-node";
import { parseMetadata } from "./cloud-node-model";

export interface CloudNodeBatchChange {
  batchChangeType: string;
  requestPayload: Record<string, any>;
}

const MAX_BATCH_ITEMS = 100;

export async function submitCloudNodeBatchChange(batchChanges: CloudNodeBatchChange[]): Promise<SubmitNodeBatchResponse> {
  if (batchChanges.length === 0) throw new Error("没有可提交的云节点批量变更");
  if (batchChanges.length > MAX_BATCH_ITEMS) throw new Error("一次最多提交 100 个云节点");

  const batchChangeType = batchChanges[0].batchChangeType;
  const first = batchChanges[0].requestPayload || {};
  let response: SubmitNodeBatchResponse;

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
          probe_enabled: params.probe_enabled,
          index
        }
      };
    });
    response = await submitCreateNodes({
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
  } else if (batchChangeType === "DEPLOY_NODE") {
    response = await submitDeployNodes({
      node_ids: batchChanges.map(item => item.requestPayload?.node_id).filter(Boolean),
      package_id: first.package_id
    });
  } else {
    throw new Error(`unsupported cloud node batch change type: ${batchChangeType}`);
  }

  if (!response.job_id) throw new Error("cloudnode 未返回 job_id");
  return response;
}
