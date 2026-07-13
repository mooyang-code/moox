import { callControl } from '@/api/admin/http';
import { gatewayURL, gatewayWebSocketURL } from '@/api/gateway';
import { issueRawSessionTicketAPI } from '@/api/modules/user';

// ========== 类型定义 ==========

export interface SSHHost {
  id?: number;
  name: string;
  address: string;
  port: number;
  user: string;
  password?: string;
  auth_type: 'pwd' | 'cert';
  net_type: 'tcp4' | 'tcp6';
  cert_data?: string;
  cert_pwd?: string;
  font_size: number;
  background: string;
  foreground: string;
  cursor_color: string;
  font_family: string;
  cursor_style: 'block' | 'underline' | 'bar';
  shell: string;
  pty_type: string;
  init_cmd?: string;
  creator?: string;
  create_time?: string;
  modify_time?: string;
}

export interface SessionInfo {
  session_id: string;
  host_id: number;
  host_name: string;
  address: string;
  port: number;
  user: string;
  client_ip: string;
  last_active_time: string;
  start_time: string;
}

export interface SftpFileItem {
  path: string;
  name: string;
  mode: string;
  size: number;
  mod_time: string;
  type: 'd' | 'f';
}

export interface PathBreadcrumb {
  name: string;
  dir: string;
}

// SSH 直连端点已并入统一网关 rawhandler，前端直连固定网关端口。

// ========== 主机配置 ==========

export const listSSHHosts = (params: { keyword?: string; offset?: number; limit?: number }) =>
  callControl<typeof params, { hosts?: SSHHost[]; total?: number }>('ssh', 'ListHosts', params);

export const createSSHHost = (data: Partial<SSHHost>) =>
  callControl<{ host: Partial<SSHHost> }, { id?: number }>('ssh', 'CreateHost', { host: data });

export const updateSSHHost = (data: Partial<SSHHost>) =>
  callControl<{ host: Partial<SSHHost> }, Record<string, never>>('ssh', 'UpdateHost', { host: data });

export const deleteSSHHost = (id: number) =>
  callControl<{ id: number }, Record<string, never>>('ssh', 'DeleteHost', { id });

export const getSSHHostDetail = (id: number) =>
  callControl<{ id: number }, { host?: SSHHost }>('ssh', 'GetHost', { id });

// ========== SSH 会话 ==========

export const createSSHSession = (data: { host_id: number }) =>
  callControl<typeof data, { session_id?: string }>('ssh', 'CreateSession', data);

export const disconnectSSHSession = (sessionId: string) =>
  callControl<{ session_id: string }, Record<string, never>>('ssh', 'DisconnectSession', { session_id: sessionId });

export const resizeSSHTerminal = (sessionId: string, w: number, h: number) =>
  callControl<{ session_id: string; w: number; h: number }, Record<string, never>>('ssh', 'ResizeWindow', { session_id: sessionId, w, h });

// ========== SFTP ==========

export const sftpList = (sessionId: string, path: string) =>
  callControl<{ session_id: string; path: string }, { files?: SftpFileItem[]; paths?: PathBreadcrumb[]; current_dir?: string }>(
    'ssh',
    'SftpList',
    { session_id: sessionId, path }
  );

export const sftpMkdir = (sessionId: string, path: string) =>
  callControl<{ session_id: string; path: string }, Record<string, never>>('ssh', 'SftpMkdir', { session_id: sessionId, path });

export const sftpDelete = (sessionId: string, path: string) =>
  callControl<{ session_id: string; path: string }, Record<string, never>>('ssh', 'SftpDelete', { session_id: sessionId, path });

// 文件下载/上传走统一网关 rawhandler（/api/admin/ssh/SftpDownload|SftpUpload）
export const getSftpDownloadUrl = async (sessionId: string, path: string) => {
  const { ticket } = await issueRawSessionTicketAPI('sftp_download', sessionId);
  return gatewayURL(`/api/admin/ssh/SftpDownload?ticket=${encodeURIComponent(ticket)}&session_id=${sessionId}&path=${encodeURIComponent(path)}`);
};

export const getSftpUploadUrl = async (sessionId: string) => {
  const { ticket } = await issueRawSessionTicketAPI('sftp_upload', sessionId);
  return gatewayURL(`/api/admin/ssh/SftpUpload?ticket=${encodeURIComponent(ticket)}&session_id=${encodeURIComponent(sessionId)}`);
};

// WebSocket 连接地址（统一网关 rawhandler，ws 协议使用固定网关端口）
export const getSSHWebSocketUrl = async (sessionId: string, w: number, h: number) => {
  const { ticket } = await issueRawSessionTicketAPI('ssh_ws', sessionId);
  return gatewayWebSocketURL(`/api/admin/ssh/WsConnect?ticket=${encodeURIComponent(ticket)}&session_id=${sessionId}&w=${w}&h=${h}`);
};

// ========== 会话管理 ==========

export const getOnlineSessions = () =>
  callControl<Record<string, never>, { sessions?: SessionInfo[] }>('ssh', 'GetOnlineSessions', {});

export const forceDisconnect = (sessionId: string) =>
  callControl<{ session_id: string }, Record<string, never>>('ssh', 'ForceDisconnect', { session_id: sessionId });
