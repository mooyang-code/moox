import { api } from '@/api/config';

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

export interface SftpListResult {
  files: SftpFileItem[];
  file_count: number;
  dir_count: number;
  paths: { name: string; dir: string }[];
  current_dir: string;
}

// ========== 获取当前主机地址 ==========

const getCurrentHost = () => {
  return window.location.hostname;
};

// SSH 独立服务端口
const SSH_DIRECT_PORT = 20180;

// ========== 主机配置 ==========

export const listSSHHosts = (params: { keyword?: string; offset?: number; limit?: number }) =>
  api.post('/ssh/ListHosts', params);

export const createSSHHost = (data: Partial<SSHHost>) =>
  api.post('/ssh/CreateHost', data);

export const updateSSHHost = (data: Partial<SSHHost>) =>
  api.post('/ssh/UpdateHost', data);

export const deleteSSHHost = (id: number) =>
  api.post('/ssh/DeleteHost', { id });

export const getSSHHostDetail = (id: number) =>
  api.get('/ssh/GetHostDetail', { params: { id } });

// ========== SSH 会话 ==========

export const createSSHSession = (data: { host_id: number }) =>
  api.post('/ssh/CreateSession', data);

export const disconnectSSHSession = (sessionId: string) =>
  api.post('/ssh/DisconnectSession', { session_id: sessionId });

export const resizeSSHTerminal = (sessionId: string, w: number, h: number) =>
  api.post('/ssh/ResizeWindow', { session_id: sessionId, w, h });

export const execSSHCommand = (sessionId: string, cmd: string) =>
  api.post('/ssh/ExecCommand', { session_id: sessionId, cmd });

// ========== SFTP ==========

export const sftpList = (sessionId: string, path: string) =>
  api.post('/ssh/SftpList', { session_id: sessionId, path });

export const sftpMkdir = (sessionId: string, path: string) =>
  api.post('/ssh/SftpMkdir', { session_id: sessionId, path });

export const sftpDelete = (sessionId: string, path: string) =>
  api.post('/ssh/SftpDelete', { session_id: sessionId, path });

// 文件下载/上传走 SSH 独立端口（非 Gateway）
export const getSftpDownloadUrl = (sessionId: string, path: string) =>
  `http://${getCurrentHost()}:${SSH_DIRECT_PORT}/api/sftp/download?session_id=${sessionId}&path=${encodeURIComponent(path)}`;

export const getSftpUploadUrl = () =>
  `http://${getCurrentHost()}:${SSH_DIRECT_PORT}/api/sftp/upload`;

// WebSocket 连接地址
export const getSSHWebSocketUrl = (sessionId: string, w: number, h: number) =>
  `ws://${getCurrentHost()}:${SSH_DIRECT_PORT}/api/ssh/conn?session_id=${sessionId}&w=${w}&h=${h}`;

// ========== 会话管理 ==========

export const getOnlineSessions = () =>
  api.post('/ssh/GetOnlineSessions', {});

export const forceDisconnect = (sessionId: string) =>
  api.post('/ssh/ForceDisconnect', { session_id: sessionId });
