import { call } from "@/api/http";
import { computeFileMd5 } from "@/utils/md5";

/** 代码包状态：pending 已发上传地址，available COS 对象已核对、可以部署。 */
export type PackageStatus = "pending" | "available";

/** 云函数代码包（全局共享，不分空间）。Collector 每个包名只保留最新 3 个。 */
export interface FunctionPackage {
  package_id: string;
  package_name: string;
  version: string;
  runtime: string;
  file_size: number | string;
  file_md5: string;
  cloud_account_id: string;
  cos_region: string;
  cos_bucket: string;
  cos_path: string;
  status: PackageStatus | string;
  /** 创建、更新时刻：Unix 毫秒（数字字符串）。 */
  created_at?: string;
  updated_at?: string;
}

export interface UploadPackageRequest {
  package_name: string;
  version: string;
  runtime: string;
}

interface InitPackageUploadResponse {
  package?: FunctionPackage;
  upload_url: string;
  /** 上传地址的过期时刻：Unix 毫秒（数字字符串）。 */
  expires_at?: string;
}

const PACKAGE_STATUS_LABEL: Record<string, string> = { pending: "待上传", available: "可用" };

export const packageStatusLabel = (status: string) => PACKAGE_STATUS_LABEL[status] || status || "未知";

export function formatFileSize(value: number | string) {
  const size = Number(value) || 0;
  if (size < 1024) return `${size}B`;
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)}KB`;
  if (size < 1024 * 1024 * 1024) return `${(size / (1024 * 1024)).toFixed(1)}MB`;
  return `${(size / (1024 * 1024 * 1024)).toFixed(1)}GB`;
}

export const uploadFunctionPackage = async (data: UploadPackageRequest, file: File) => {
  const initRsp = await call<UploadPackageRequest & { filename: string }, InitPackageUploadResponse>(
    "collector",
    "InitPackageUpload",
    { ...data, filename: file.name }
  );
  const packageId = initRsp.package?.package_id || "";
  if (!packageId || !initRsp.upload_url) throw new Error("Collector 没有返回上传地址");
  const putResp = await fetch(initRsp.upload_url, { method: "PUT", body: file });
  if (!putResp.ok) {
    throw new Error(`上传到 COS 失败：HTTP ${putResp.status}`);
  }
  const md5Hex = await computeFileMd5(file);
  const rsp = await call<{ package_id: string; file_size: number; file_md5: string }, { package?: FunctionPackage }>(
    "collector",
    "CompletePackageUpload",
    { package_id: packageId, file_size: file.size, file_md5: md5Hex }
  );
  return rsp.package ?? null;
};

/** 全局代码包列表，按上传先后倒序，不分页。 */
export const listFunctionPackages = async (): Promise<FunctionPackage[]> => {
  const rsp = await call<Record<string, never>, { packages?: FunctionPackage[] }>("collector", "ListFunctionPackages", {});
  return rsp.packages ?? [];
};

export const deleteFunctionPackage = async (packageId: string): Promise<void> => {
  await call<{ package_id: string }, Record<string, never>>("collector", "DeleteFunctionPackage", {
    package_id: packageId
  });
};
