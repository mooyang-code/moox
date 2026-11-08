import { PACKAGE_STATUS_LABEL, PACKAGE_TYPE_LABEL } from "@/api/function-package";
import { formatInstant } from "@/utils/time-zone";

export interface FunctionInstance {
  instance_id: string;
  cloud_account_id: string;
  namespace: string;
  function_type: string;
  trigger_type: string;
  region: string;
  package_id?: string;
  deployment_id?: string;
  package_version?: string;
  metadata: string | Record<string, unknown>;
  create_time?: string;
  modify_time?: string;
}

export interface RegionInfo {
  code: string;
  name: string;
  tag: string;
  max_instances?: number;
}

export function normalizeFunctionInstances(items: Array<Partial<FunctionInstance>>): FunctionInstance[] {
  return items.map(item => ({
    instance_id: String(item.instance_id || ""),
    cloud_account_id: String(item.cloud_account_id || ""),
    namespace: String(item.namespace || ""),
    function_type: String(item.function_type || ""),
    trigger_type: String(item.trigger_type || ""),
    region: String(item.region || ""),
    package_id: String(item.package_id || ""),
    deployment_id: String(item.deployment_id || ""),
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

export const getProviderName = (value: string) => ({ tencent: "腾讯云", aliyun: "阿里云", aws: "AWS" })[value] || value;
export const getFunctionTypeLabel = (value: string) =>
  ({ "scf-event": "云函数（事件型）", "scf-web": "云函数（Web型）", server: "服务器" })[value] || value;
export const getFunctionTypeColor = (value: string) =>
  ({ "scf-event": "blue", "scf-web": "cyan", server: "orange" })[value] || "gray";
export const getTriggerTypeLabel = (value: string) => ({ timer: "定时器", invoke: "手动调用" })[value] || value || "-";

export function getTimerAvailableLabel(value: unknown) {
  const normalized = String(value || "").toLowerCase();
  if (normalized === "available") return "可用";
  if (normalized === "") return "未知";
  return String(value);
}
export const getTimerAvailableColor = (value: unknown) =>
  String(value || "").toLowerCase() === "available" ? "green" : "orange";

export function getPackageTypeColor(value: number | string) {
  return (
    (
      {
        "1": "blue",
        "2": "green",
        "3": "gray",
        PACKAGE_TYPE_COLLECTOR: "blue",
        PACKAGE_TYPE_FACTOR: "green",
        PACKAGE_TYPE_CUSTOM: "gray",
        data_collector: "blue",
        factor_calculator: "green"
      } as Record<string, string>
    )[String(value)] || "gray"
  );
}
// PackageStatus 枚举：2 = 可用（已上传完成），只有可用的代码包能下载。
export const PACKAGE_STATUS_AVAILABLE = 2;

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

/** 带时区后缀的时间：执行器属于空间，用空间时区；代码包这类全局对象用 UTC。 */
export function formatDateTime(value: string | undefined, zone: string) {
  return formatInstant(value, zone);
}

export function formatMetadata(metadata: string | Record<string, unknown>) {
  if (!metadata) return "-";
  if (typeof metadata === "object") return JSON.stringify(metadata, null, 2);
  try {
    return JSON.stringify(JSON.parse(metadata), null, 2);
  } catch {
    return metadata;
  }
}
