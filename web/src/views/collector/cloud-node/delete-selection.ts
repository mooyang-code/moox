import type { CloudNodeDeleteSnapshot } from "@/api/cloud-node";
import type { CloudNode } from "./cloud-node-model";

export type CloudNodeDeleteSnapshotMap = Record<string, CloudNodeDeleteSnapshot>;

export function updateDeleteSelection(
  current: CloudNodeDeleteSnapshotMap,
  nodes: CloudNode[],
  selected: boolean
): CloudNodeDeleteSnapshotMap {
  const next = { ...current };
  for (const node of nodes) {
    if (selected) {
      if (!next[node.node_id]) {
        next[node.node_id] = {
          node_id: node.node_id,
          package_id: node.package_id,
          deployment_id: node.deployment_id,
          modify_time: node.modify_time
        };
      }
    } else {
      delete next[node.node_id];
    }
  }
  return next;
}

export function captureDeleteSelection(keys: string[], snapshots: CloudNodeDeleteSnapshotMap): CloudNodeDeleteSnapshot[] {
  return keys.map(nodeId => {
    const snapshot = snapshots[nodeId];
    if (!snapshot) throw new Error(`节点 ${nodeId} 缺少选择时的身份快照`);
    return { ...snapshot };
  });
}
