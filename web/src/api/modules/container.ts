import axios from '@/api/index';

// 容器SSH相关API接口

/**
 * 创建SSH会话
 */
export const createSSHSession = (data: {
  container_id: string;
  container_name: string;
  user?: string;
  shell?: string;
  pty_type?: string;
}) => {
  return axios({
    url: '/api/container/ssh/create_session',
    method: 'post',
    data
  });
};

/**
 * 断开SSH连接
 */
export const disconnectSSH = (sessionId: string) => {
  return axios({
    url: `/api/container/ssh/disconnect?session_id=${sessionId}`,
    method: 'post'
  });
};

/**
 * 执行命令
 */
export const executeCommand = (data: {
  session_id: string;
  cmd: string;
}) => {
  return axios({
    url: '/api/container/ssh/exec',
    method: 'post',
    data
  });
};

/**
 * 调整终端窗口大小
 */
export const resizeTerminal = (sessionId: string, width: number, height: number) => {
  return axios({
    url: `/api/container/ssh/resize?session_id=${sessionId}&w=${width}&h=${height}`,
    method: 'patch'
  });
};

/**
 * 获取容器列表
 */
export const getContainerList = () => {
  return axios({
    url: '/api/container/list',
    method: 'get'
  });
};

/**
 * 获取容器详情
 */
export const getContainerDetail = (containerId: string) => {
  return axios({
    url: `/api/container/${containerId}`,
    method: 'get'
  });
};

/**
 * 启动容器
 */
export const startContainer = (containerId: string) => {
  return axios({
    url: `/api/container/${containerId}/start`,
    method: 'post'
  });
};

/**
 * 停止容器
 */
export const stopContainer = (containerId: string) => {
  return axios({
    url: `/api/container/${containerId}/stop`,
    method: 'post'
  });
};

/**
 * 重启容器
 */
export const restartContainer = (containerId: string) => {
  return axios({
    url: `/api/container/${containerId}/restart`,
    method: 'post'
  });
};
