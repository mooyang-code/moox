import { call } from "@/api/http";
import { gatewayURL, gatewayWebSocketURL } from "@/api/gateway";

// ========== 类型定义 ==========

export interface SftpFileItem {
  path: string;
  name: string;
  mode: string;
  size: number;
  mod_time: string;
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

// 终端 WebSocket 地址（wss 与页面同源）
export const getSSHWebSocketUrl = (sessionId: string, w: number, h: number) =>
  gatewayWebSocketURL(`/api/admin/ssh/WsConnect?session_id=${encodeURIComponent(sessionId)}&w=${w}&h=${h}`);
