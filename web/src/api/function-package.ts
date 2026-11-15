import { readSelectedSpaceId } from "./admin/space-header";
import { callControl } from "@/api/admin/http";
import forge from "node-forge";

/** 代码包状态：pending 已发上传地址，available COS 对象已核对、可以部署。 */
export type PackageStatus = "pending" | "available";

/** 云函数代码包（属于空间）。Collector 每个包名只保留最新 3 个。 */
export interface FunctionPackage {
  space_id: string;
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
  create_time?: string;
  modify_time?: string;
}

export interface UploadPackageRequest {
  package_name: string;
  version: string;
  runtime: string;
  cloud_account_id: string;
}

interface InitPackageUploadResponse {
  package?: FunctionPackage;
  upload_url: string;
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

// 上传流程的各个请求显式带上发起时的空间（请求头与请求体是同一个值）：用户在上传期间切换空间时，完成请求仍要落在包所属的空间。
const spaceHeaders = (spaceId: string) => (spaceId ? { headers: { "X-Space-Id": spaceId } } : undefined);

export const uploadFunctionPackage = async (data: UploadPackageRequest, file: File, spaceId = readSelectedSpaceId()) => {
  const initRsp = await callControl<UploadPackageRequest & { space_id: string; filename: string }, InitPackageUploadResponse>(
    "collector",
    "InitPackageUpload",
    { ...data, space_id: spaceId, filename: file.name },
    spaceHeaders(spaceId)
  );
  const packageId = initRsp.package?.package_id || "";
  if (!packageId || !initRsp.upload_url) throw new Error("Collector 没有返回上传地址");
  const putResp = await fetch(initRsp.upload_url, { method: "PUT", body: file });
  if (!putResp.ok) {
    throw new Error(`上传到 COS 失败：HTTP ${putResp.status}`);
  }
  const bytes = new Uint8Array(await file.arrayBuffer());
  let binary = "";
  for (let i = 0; i < bytes.length; i++) {
    binary += String.fromCharCode(bytes[i]);
  }
  const md5Hex = forge.md.md5.create().update(binary).digest().toHex();
  const rsp = await callControl<
    { space_id: string; package_id: string; file_size: number; file_md5: string },
    { package?: FunctionPackage }
  >(
    "collector",
    "CompletePackageUpload",
    { space_id: spaceId, package_id: packageId, file_size: file.size, file_md5: md5Hex },
    spaceHeaders(spaceId)
  );
  return rsp.package ?? null;
};

/** 按上传先后倒序，不分页。 */
export const listFunctionPackages = async (spaceId: string): Promise<FunctionPackage[]> => {
  const rsp = await callControl<{ space_id: string }, { packages?: FunctionPackage[] }>(
    "collector",
    "ListFunctionPackages",
    { space_id: spaceId },
    spaceHeaders(spaceId)
  );
  return rsp.packages ?? [];
};

export const deleteFunctionPackage = async (spaceId: string, packageId: string): Promise<void> => {
  await callControl<{ space_id: string; package_id: string }, Record<string, never>>(
    "collector",
    "DeleteFunctionPackage",
    { space_id: spaceId, package_id: packageId },
    spaceHeaders(spaceId)
  );
};
