import { PACKAGE_STATUS_LABEL, PACKAGE_TYPE_LABEL } from "@/api/function-package";

export interface CloudNode {
  node_id: string;
  cloud_account_id: string;
  namespace: string;
  node_type: string;
  trigger_type: string;
  region: string;
  tag: string;
  ip_address: string;
  package_id?: string;
  package_version?: string;
  metadata: string | Record<string, unknown>;
  create_time?: string;
  modify_time?: string;
}

export interface RegionInfo {
  code: string;
  name: string;
  tag: string;
  max_nodes?: number;
}

export interface BatchPlanItem {
  regionCode: string;
  regionName: string;
  tag: string;
  maxNodes: number;
  usedNodes: number;
  availableNodes: number;
  planCount: number;
}

export function normalizeCloudNodes(items: Array<Partial<CloudNode>>): CloudNode[] {
  return items.map(item => ({
    node_id: String(item.node_id || ""),
    cloud_account_id: String(item.cloud_account_id || ""),
    namespace: String(item.namespace || ""),
    node_type: String(item.node_type || ""),
    trigger_type: String(item.trigger_type || ""),
    region: String(item.region || ""),
    tag: String(item.tag || ""),
    ip_address: String(item.ip_address || ""),
    package_id: String(item.package_id || ""),
    package_version: String(item.package_version || ""),
    metadata: (item.metadata as string | Record<string, unknown>) || "",
    create_time: String(item.create_time || ""),
    modify_time: String(item.modify_time || "")
  }));
}

export function parseMetadata(value: unknown): Record<string, unknown> {
  if (!value) return {};
  if (typeof value === "string") {
    try {
      const parsed = JSON.parse(value);
      return parsed && typeof parsed === "object" ? parsed : {};
    } catch {
      return {};
    }
  }
  return typeof value === "object" ? (value as Record<string, unknown>) : {};
}

export const getBatchChangeTypeText = (value: string) =>
  (
    ({
      CREATE_NODE: "批量创建节点",
      DEPLOY_NODE: "批量部署节点",
      NODE_BATCH_OPERATION_CREATE_NODES: "批量创建节点",
      NODE_BATCH_OPERATION_DEPLOY_NODES: "批量部署节点"
    }) as Record<string, string>
  )[value] || value;

export const getProviderName = (value: string) => ({ tencent: "腾讯云", aliyun: "阿里云", aws: "AWS" })[value] || value;
export const getNodeTypeLabel = (value: string) =>
  ({ "scf-event": "云函数（事件型）", "scf-web": "云函数（Web型）", server: "服务器" })[value] || value;
export const getNodeTypeColor = (value: string) =>
  ({ "scf-event": "blue", "scf-web": "cyan", server: "orange" })[value] || "gray";
export const getTriggerTypeLabel = (value: string) => ({ timer: "定时器", invoke: "手动调用" })[value] || (value || "-");

export const getCollectorName = (value: string) =>
  ({ kline: "K线", ticker: "行情", orderbook: "订单簿", trade: "逐笔", news: "资讯", symbol: "标的" })[value] || value;
export const getCollectorColor = (value: string) =>
  ({ kline: "blue", ticker: "green", orderbook: "orange", trade: "purple", news: "red", symbol: "cyan" })[value] || "gray";

export function getPackageTypeColor(value: number | string) {
  return (
    (
      {
        "1": "blue",
        "2": "green",
        "3": "gray",
        PACKAGE_TYPE_COLLECTOR: "blue",
        PACKAGE_TYPE_FACTOR: "green",
        PACKAGE_TYPE_CUSTOM: "gray"
      } as Record<string, string>
    )[String(value)] || "gray"
  );
}
export function getPackageStatusColor(value: number | string) {
  return ({ 1: "blue", 2: "green", 3: "red", 4: "gray" } as Record<number, string>)[Number(value)] || "gray";
}
export function getPackageStatusLabel(value: number | string) {
  const key =
    (
      {
        1: "PACKAGE_STATUS_PENDING",
        2: "PACKAGE_STATUS_AVAILABLE",
        3: "PACKAGE_STATUS_FAILED",
        4: "PACKAGE_STATUS_DELETED"
      } as Record<number, string>
    )[Number(value)] || "PACKAGE_STATUS_UNSPECIFIED";
  return PACKAGE_STATUS_LABEL[key] || "未知";
}
export function getPackageTypeLabel(value: number | string) {
  if (typeof value === "string" && PACKAGE_TYPE_LABEL[value]) return PACKAGE_TYPE_LABEL[value];
  const key =
    ({ 1: "PACKAGE_TYPE_COLLECTOR", 2: "PACKAGE_TYPE_FACTOR", 3: "PACKAGE_TYPE_CUSTOM" } as Record<number, string>)[
      Number(value)
    ] || "PACKAGE_TYPE_UNSPECIFIED";
  return PACKAGE_TYPE_LABEL[key] || String(value);
}

export function formatFileSize(size: number) {
  if (size < 1024) return `${size}B`;
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)}KB`;
  if (size < 1024 * 1024 * 1024) return `${(size / (1024 * 1024)).toFixed(1)}MB`;
  return `${(size / (1024 * 1024 * 1024)).toFixed(1)}GB`;
}

export function formatDateTime(value?: string) {
  if (!value) return "-";
  try {
    return new Date(value).toLocaleString("zh-CN", {
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit"
    });
  } catch {
    return value;
  }
}

export const formatTime = formatDateTime;

export function formatMetadata(metadata: string | Record<string, unknown>) {
  if (!metadata) return "-";
  if (typeof metadata === "object") return JSON.stringify(metadata, null, 2);
  try {
    return JSON.stringify(JSON.parse(metadata), null, 2);
  } catch {
    return metadata;
  }
}
