import { gatewayURL } from "@/api/gateway";
import { issueRawSessionTicketAPI, type RawSessionTicketOperation } from "@/api/modules/user";
import type { ArchiveRecentRowsRsp } from "./types";

/**
 * 归档分区下载与最新数据走 Admin 的裸 HTTP（/api/admin/archive/PartitionDownload|RecentRows），由 Admin 转给归档执行器的数据端口。
 * 鉴权用一次性 ticket：资源填 "<空间>/<数据集>"，与 query 里的 space、dataset 必须一致；每次下载、每翻一页都要重新取。
 * 失败时响应体是 JSON {code, msg, success}，msg 是给人看的中文，抛出的 ArchiveRawError 带上它和状态码。
 */
export class ArchiveRawError extends Error {
  constructor(
    message: string,
    readonly status: number
  ) {
    super(message);
    this.name = "ArchiveRawError";
  }
}

/** 响应体读不出 msg 时按状态码给的说明，与 Admin 转发器、执行器数据端口的错误口径一致。 */
const STATUS_TEXT: Record<number, string> = {
  404: "执行器上找不到这个任务或分区",
  409: "远端副本或数据集说明还没有就绪",
  429: "同时下载的数量已达上限，请稍后再试",
  501: "归档执行器所在主机不支持网页下载与预览（只支持 control 与只出站主机）",
  503: "拓扑里没有启用的归档执行器部署",
  502: "连接归档执行器失败，确认执行器在运行",
  504: "归档执行器响应超时"
};

async function rawError(response: Response): Promise<ArchiveRawError> {
  let message = "";
  try {
    const body = JSON.parse(await response.text()) as { msg?: unknown };
    if (typeof body.msg === "string") message = body.msg;
  } catch {
    // 不是 JSON（例如前面的代理直接返回了网页）：按状态码说明
  }
  return new ArchiveRawError(message || STATUS_TEXT[response.status] || `请求失败（HTTP ${response.status}）`, response.status);
}

async function rawURL(operation: RawSessionTicketOperation, method: string, params: Record<string, string | number | undefined>) {
  const space = String(params.space || "");
  const dataset = String(params.dataset || "");
  const { ticket } = await issueRawSessionTicketAPI(operation, `${space}/${dataset}`);
  const query = Object.entries({ ticket, ...params })
    .filter(([, value]) => value !== undefined && value !== "")
    .map(([key, value]) => `${encodeURIComponent(key)}=${encodeURIComponent(String(value))}`)
    .join("&");
  return gatewayURL(`/api/admin/archive/${method}?${query}`);
}

export interface ArchiveRecentRowsReq {
  space: string;
  dataset: string;
  subject: string;
  /** RFC3339；只取 data_time 早于它的行（翻页），为空取最新 */
  before?: string;
  /** 默认 200，最多 500 */
  limit?: number;
}

/** 某个标的在执行器暂存库里最近的行（按 data_time 倒序）。 */
export async function fetchArchiveRecentRows(req: ArchiveRecentRowsReq, signal?: AbortSignal): Promise<ArchiveRecentRowsRsp> {
  const url = await rawURL("archive_recent", "RecentRows", { ...req });
  const response = await fetch(url, { signal, cache: "no-store" });
  if (!response.ok) throw await rawError(response);
  return (await response.json()) as ArchiveRecentRowsRsp;
}

export interface ArchiveDownloadReq {
  space: string;
  dataset: string;
  partition: string;
  /** 响应头里没有文件名时用它 */
  fallbackName: string;
}

export interface ArchiveDownload {
  blob: Blob;
  fileName: string;
}

/** 从 Content-Disposition 取文件名（只认 filename="…"，执行器只写这一种）。 */
export function dispositionFileName(header: string | null): string {
  const match = /filename="([^"]+)"/i.exec(header || "");
  return match ? match[1] : "";
}

/**
 * 下载分区的远端副本。先看状态码，失败时把执行器给的原因抛出来；成功时边读边报告进度，读完核对长度。
 * 执行器发现校验和不符时会中断连接，这里得到的是读取失败或长度不足，都按“下载不完整”报错，不交出残缺文件。
 */
export async function downloadArchivePartition(
  req: ArchiveDownloadReq,
  onProgress?: (received: number, total: number) => void,
  signal?: AbortSignal
): Promise<ArchiveDownload> {
  const { fallbackName, ...params } = req;
  const url = await rawURL("archive_download", "PartitionDownload", params);
  const response = await fetch(url, { signal, cache: "no-store" });
  if (!response.ok) throw await rawError(response);
  // 链路上某层做了内容压缩时，Content-Length 是压缩后的长度，与读到的（已解压）字节数不可比，不核对
  const encoding = (response.headers.get("Content-Encoding") || "identity").trim().toLowerCase();
  const total = encoding === "identity" ? Number(response.headers.get("Content-Length") || 0) : 0;
  const chunks: BlobPart[] = [];
  let received = 0;
  try {
    const reader = response.body?.getReader();
    if (!reader) throw new Error("浏览器不支持流式读取");
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      chunks.push(value);
      received += value.byteLength;
      onProgress?.(received, total);
    }
  } catch (cause) {
    if (signal?.aborted) throw cause;
    throw new ArchiveRawError("下载不完整：连接中断，请重试", 0);
  }
  if (total > 0 && received !== total) throw new ArchiveRawError("下载不完整：收到的字节数与文件大小不符，请重试", 0);
  return {
    blob: new Blob(chunks, { type: response.headers.get("Content-Type") || "application/gzip" }),
    fileName: dispositionFileName(response.headers.get("Content-Disposition")) || fallbackName
  };
}

/** 把下载好的文件交给浏览器保存。 */
export function saveBlob(blob: Blob, fileName: string) {
  const href = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = href;
  a.download = fileName;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  // 留一点时间给浏览器开始保存再释放
  setTimeout(() => URL.revokeObjectURL(href), 60_000);
}
