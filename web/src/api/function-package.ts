import { callControl } from "@/api/admin/http";
import forge from "node-forge";
export { withOptionalSpace } from "@/api/space-context";

export type PackageStatus =
  | "PACKAGE_STATUS_UNSPECIFIED"
  | "PACKAGE_STATUS_PENDING"
  | "PACKAGE_STATUS_AVAILABLE"
  | "PACKAGE_STATUS_FAILED"
  | "PACKAGE_STATUS_DELETED";
export type PackageType = "PACKAGE_TYPE_UNSPECIFIED" | "PACKAGE_TYPE_COLLECTOR" | "PACKAGE_TYPE_FACTOR" | "PACKAGE_TYPE_CUSTOM";

export interface FunctionPackage {
  id: number;
  space_id?: string;
  package_id: string;
  package_name: string;
  version: string;
  description: string;
  runtime: string;
  package_type: PackageType | number;
  biz_type: string;
  original_filename?: string;
  file_size: number;
  file_md5: string;
  cloud_account_id: string;
  cos_region: string;
  cos_bucket: string;
  cos_path: string;
  cos_url?: string;
  status: PackageStatus | number;
  upload_progress?: number;
  error_message?: string;
  last_deploy_time?: string;
  created_by?: string;
  is_deleted?: boolean;
  created_time?: string;
  updated_time?: string;
}

export interface UploadPackageRequest {
  package_name: string;
  version: string;
  description?: string;
  runtime: string;
  package_type: PackageType | number;
  biz_type?: string;
  original_filename?: string;
  cloud_account_id?: string;
}

export interface PackageListRequest {
  package_name?: string;
  runtime?: string;
  package_type?: PackageType | number;
  biz_type?: string;
  status?: PackageStatus | number;
  page?: number | { page?: number; size?: number };
  page_size?: number;
}

export interface InitPackageUploadResponse {
  package_id: string;
  upload_url: string;
  cos_path: string;
  expires_at: number;
}

export const PACKAGE_STATUS_LABEL: Record<string, string> = {
  PACKAGE_STATUS_PENDING: "待上传",
  PACKAGE_STATUS_AVAILABLE: "可用",
  PACKAGE_STATUS_FAILED: "失败",
  PACKAGE_STATUS_DELETED: "已删除",
  PACKAGE_STATUS_UNSPECIFIED: "未知"
};

export const PACKAGE_TYPE_LABEL: Record<string, string> = {
  PACKAGE_TYPE_COLLECTOR: "采集器",
  PACKAGE_TYPE_FACTOR: "因子",
  PACKAGE_TYPE_CUSTOM: "自定义",
  PACKAGE_TYPE_UNSPECIFIED: "未指定"
};

export const RUNTIME_OPTIONS = [
  { label: "Python3.9", value: "Python3.9" },
  { label: "Python3.10", value: "Python3.10" },
  { label: "CustomRuntime", value: "CustomRuntime" }
];

export const PACKAGE_TYPE_OPTIONS = [
  { label: "采集器", value: 1 },
  { label: "因子", value: 2 },
  { label: "自定义", value: 3 }
];

export const LEGACY_PACKAGE_TYPE: Record<string, number> = {
  data_collector: 1,
  factor_calculator: 2,
  collector: 1,
  factor: 2,
  custom: 3
};

export const resolvePackageType = (value?: string | number): number => {
  if (typeof value === "number" && value > 0) {
    return value;
  }
  if (typeof value === "string" && value) {
    const numeric = Number(value);
    if (!Number.isNaN(numeric) && numeric > 0) {
      return numeric;
    }
    return LEGACY_PACKAGE_TYPE[value] ?? 1;
  }
  return 1;
};

export const STATUS_OPTIONS = [
  { label: "待上传", value: 1 },
  { label: "可用", value: 2 },
  { label: "失败", value: 3 },
  { label: "已删除", value: 4 }
];

export const initPackageUpload = async (data: UploadPackageRequest): Promise<InitPackageUploadResponse> => {
  return callControl<UploadPackageRequest, InitPackageUploadResponse>("cloudnode", "InitPackageUpload", data);
};

export const completePackageUpload = async (
  packageId: string,
  fileMd5: string,
  fileSize: number
): Promise<FunctionPackage | null> => {
  const rsp = await callControl<{ package_id: string; file_md5: string; file_size: number }, { detail?: FunctionPackage }>(
    "cloudnode",
    "CompletePackageUpload",
    { package_id: packageId, file_md5: fileMd5, file_size: fileSize }
  );
  return rsp.detail ?? null;
};

export const uploadFunctionPackage = async (data: UploadPackageRequest, file: File) => {
  const initRsp = await initPackageUpload({ ...data, original_filename: file.name });
  const putResp = await fetch(initRsp.upload_url, { method: "PUT", body: file });
  if (!putResp.ok) {
    throw new Error(`COS upload failed: ${putResp.status}`);
  }
  const buffer = await file.arrayBuffer();
  const bytes = new Uint8Array(buffer);
  let binary = "";
  for (let i = 0; i < bytes.length; i++) {
    binary += String.fromCharCode(bytes[i]);
  }
  const md5Hex = forge.md.md5.create().update(binary).digest().toHex();
  const detail = await completePackageUpload(initRsp.package_id, md5Hex, file.size);
  return { package_id: initRsp.package_id, detail };
};

export const getFunctionPackageList = async (
  params: PackageListRequest = {}
): Promise<{ total: number; items: FunctionPackage[] }> => {
  const raw = params;
  const normalized: PackageListRequest = { ...raw };
  if (typeof raw.page === "number" || raw.page_size !== undefined) {
    const pageNum = typeof raw.page === "number" ? raw.page : 1;
    normalized.page = { page: pageNum, size: raw.page_size ?? 10 };
  }
  delete normalized.page_size;
  const rsp = await callControl<PackageListRequest, { items?: FunctionPackage[]; page?: { total?: number } }>(
    "cloudnode",
    "GetPackageList",
    normalized
  );
  return { total: rsp.page?.total ?? 0, items: rsp.items ?? [] };
};

export const getFunctionPackageDetail = async (packageId: string): Promise<FunctionPackage | null> => {
  const rsp = await callControl<{ package_id: string }, { detail?: FunctionPackage }>("cloudnode", "GetPackageDetail", {
    package_id: packageId
  });
  return rsp.detail ?? null;
};

export const deleteFunctionPackage = async (packageId: string): Promise<void> => {
  await callControl<{ package_id: string }, Record<string, never>>("cloudnode", "DeletePackage", { package_id: packageId });
};

export const getPackageDownloadURL = async (packageId: string) => {
  const rsp = await callControl<{ package_id: string }, { url?: { download_url?: string } }>(
    "cloudnode",
    "GetPackageDownloadURL",
    { package_id: packageId }
  );
  return rsp.url;
};

export const getPackageDownloadLink = async (packageId: string) => {
  const url = await getPackageDownloadURL(packageId);
  return url?.download_url || "";
};

export const downloadPackageByURL = async (packageId: string): Promise<void> => {
  const downloadURL = await getPackageDownloadLink(packageId);
  if (!downloadURL) {
    throw new Error("下载地址为空");
  }
  const link = document.createElement("a");
  link.href = downloadURL;
  link.download = "";
  link.rel = "noopener";
  document.body.appendChild(link);
  link.click();
  document.body.removeChild(link);
};
