import type { FunctionInstanceDeleteSnapshot } from "@/api/function-instance";
import type { FunctionInstance } from "./function-instance-model";

export type FunctionInstanceDeleteSnapshotMap = Record<string, FunctionInstanceDeleteSnapshot>;

export function updateDeleteSelection(
  current: FunctionInstanceDeleteSnapshotMap,
  instances: FunctionInstance[],
  selected: boolean
): FunctionInstanceDeleteSnapshotMap {
  const next = { ...current };
  for (const instance of instances) {
    if (selected) {
      if (!next[instance.instance_id]) {
        next[instance.instance_id] = {
          instance_id: instance.instance_id,
          package_id: instance.package_id,
          deployment_id: instance.deployment_id,
          modify_time: instance.modify_time
        };
      }
    } else {
      delete next[instance.instance_id];
    }
  }
  return next;
}

export function captureDeleteSelection(
  keys: string[],
  snapshots: FunctionInstanceDeleteSnapshotMap
): FunctionInstanceDeleteSnapshot[] {
  return keys.map(instanceId => {
    const snapshot = snapshots[instanceId];
    if (!snapshot) throw new Error(`实例 ${instanceId} 缺少选择时的身份快照`);
    return { ...snapshot };
  });
}
