import { call } from "@/api/http";
import { gatewayURL, gatewayWebSocketURL } from "@/api/gateway";
import { expireBrowserSession } from "@/api/admin/session-expiry";
import type { AuthSessionExpiredError } from "@/api/admin/auth-errors";

// ========== 类型定义 ==========

export interface SftpFileItem {
  path: string;
  name: string;
  mode: string;
  size: number;
  /** 修改时刻：Unix 毫秒的数字字符串 */
  modified_at: string;
  type: "d" | "f";
}

export interface PathBreadcrumb {
  name: string;
  dir: string;
}

// SSH 直连端点（终端 WebSocket、文件上传下载）是管理台的裸路由，与页面同源，浏览器自动带上会话 Cookie，不需要票据。

// ========== SSH 会话 ==========

export const createSSHSession = (data: { host_id: string }) =>
  call<typeof data, { session_id?: string }>("ssh", "CreateSession", data);

export const disconnectSSHSession = (sessionId: string) =>
  call<{ session_id: string }, Record<string, never>>("ssh", "DisconnectSession", { session_id: sessionId });

export const resizeSSHTerminal = (sessionId: string, w: number, h: number) =>
  call<{ session_id: string; w: number; h: number }, Record<string, never>>("ssh", "ResizeWindow", {
    session_id: sessionId,
    w,
    h
  });

// ========== SFTP ==========

export const sftpList = (sessionId: string, path: string) =>
  call<{ session_id: string; path: string }, { files?: SftpFileItem[]; paths?: PathBreadcrumb[]; current_dir?: string }>(
    "ssh",
    "SftpList",
    { session_id: sessionId, path }
  );

export const sftpMkdir = (sessionId: string, path: string) =>
  call<{ session_id: string; path: string }, Record<string, never>>("ssh", "SftpMkdir", { session_id: sessionId, path });

export const sftpDelete = (sessionId: string, path: string) =>
  call<{ session_id: string; path: string }, Record<string, never>>("ssh", "SftpDelete", { session_id: sessionId, path });

// 文件下载/上传：/api/admin/ssh/SftpDownload|SftpUpload
export const getSftpDownloadUrl = (sessionId: string, path: string) =>
  gatewayURL(`/api/admin/ssh/SftpDownload?session_id=${encodeURIComponent(sessionId)}&path=${encodeURIComponent(path)}`);

export const getSftpUploadUrl = (sessionId: string) =>
  gatewayURL(`/api/admin/ssh/SftpUpload?session_id=${encodeURIComponent(sessionId)}`);

export interface SftpUploadResult {
  code?: number;
  msg?: string;
  files?: string[];
  success?: boolean;
}

export interface SftpDownloadResult {
  blob: Blob;
  fileName: string;
}

/** 从 Content-Disposition 取文件名，兼容带引号与含空格的未加引号 filename=。 */
export function sftpDispositionFileName(header: string | null): string {
  if (!header) return "";
  const quoted = /filename="([^"]+)"/i.exec(header);
  if (quoted) return quoted[1];
  const bare = /filename=([^;]+)/i.exec(header);
  return bare ? bare[1].trim() : "";
}

async function sftpRawError(response: Response, fallback: string): Promise<Error | AuthSessionExpiredError> {
  if (response.status === 401) return expireBrowserSession("登录已失效，请重新登录");
  let message = "";
  try {
    const body = JSON.parse(await response.text()) as { msg?: unknown; ret_info?: { msg?: unknown } };
    if (typeof body.msg === "string") message = body.msg;
    else if (typeof body.ret_info?.msg === "string") message = body.ret_info.msg;
  } catch {
    // 非 JSON 响应按状态码回退
  }
  return new Error(message || `${fallback}（HTTP ${response.status}）`);
}

/** 通过裸 HTTP 上传文件到远程主机，支持 AbortSignal 取消，401 自动清理登录态并跳转登录页。 */
export async function sftpUpload(
  sessionId: string,
  targetPath: string,
  file: File,
  signal?: AbortSignal
): Promise<SftpUploadResult> {
  const formData = new FormData();
  formData.append("path", targetPath);
  formData.append("file", file);
  const response = await fetch(getSftpUploadUrl(sessionId), {
    method: "POST",
    body: formData,
    credentials: "same-origin",
    signal
  });
  if (!response.ok) throw await sftpRawError(response, "文件上传失败");
  return (await response.json()) as SftpUploadResult;
}

/** 通过裸 HTTP 流式下载远程文件，支持进度回调与 AbortSignal 取消，401 自动清理登录态并跳转登录页。 */
export async function sftpDownload(
  sessionId: string,
  path: string,
  fallbackName: string,
  onProgress?: (received: number, total: number) => void,
  signal?: AbortSignal
): Promise<SftpDownloadResult> {
  const response = await fetch(getSftpDownloadUrl(sessionId, path), {
    cache: "no-store",
    credentials: "same-origin",
    signal
  });
  if (!response.ok) throw await sftpRawError(response, "文件下载失败");
  const encoding = (response.headers.get("Content-Encoding") || "identity").trim().toLowerCase();
  const total = encoding === "identity" ? Number(response.headers.get("Content-Length") || 0) : 0;
  const chunks: BlobPart[] = [];
  let received = 0;
  let reader: ReadableStreamDefaultReader<Uint8Array> | undefined;
  const onAbort = () => {
    void reader?.cancel().catch(() => undefined);
  };
  signal?.addEventListener("abort", onAbort, { once: true });
  try {
    reader = response.body?.getReader();
    if (reader) {
      for (;;) {
        if (signal?.aborted) throw new DOMException("下载已取消", "AbortError");
        const { done, value } = await reader.read();
        if (done) break;
        if (signal?.aborted) throw new DOMException("下载已取消", "AbortError");
        chunks.push(value);
        received += value.byteLength;
        onProgress?.(received, total);
      }
    } else {
      const blob = await response.blob();
      if (signal?.aborted) throw new DOMException("下载已取消", "AbortError");
      chunks.push(blob);
      received = blob.size;
      onProgress?.(received, total);
    }
  } catch (cause) {
    if (signal?.aborted) throw cause;
    throw new Error("下载不完整：连接中断，请重试");
  } finally {
    signal?.removeEventListener("abort", onAbort);
    if (signal?.aborted) {
      void (reader ? reader.cancel() : response.body?.cancel())?.catch(() => undefined);
    }
  }
  if (signal?.aborted) throw new DOMException("下载已取消", "AbortError");
  if (total > 0 && received !== total) throw new Error("下载不完整：收到的字节数与文件大小不符，请重试");
  return {
    blob: new Blob(chunks, { type: response.headers.get("Content-Type") || "application/octet-stream" }),
    fileName: sftpDispositionFileName(response.headers.get("Content-Disposition")) || fallbackName
  };
}

// 终端 WebSocket 地址（wss 与页面同源）
export const getSSHWebSocketUrl = (sessionId: string, w: number, h: number) =>
  gatewayWebSocketURL(`/api/admin/ssh/WsConnect?session_id=${encodeURIComponent(sessionId)}&w=${w}&h=${h}`);
