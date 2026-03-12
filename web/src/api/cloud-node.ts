import { api } from '@/api/config';

// 云节点接口定义
export interface CloudNode {
  node_id: string;
  node_name: string;
  node_type: string;
  cloud_account_id: string;
  region: string;
  namespace: string;
  function_name: string;
  runtime: string;
  status: string;
  status_desc: string;
  load_level: number;
  package_id?: string; 
  package_name?: string;
  package_version?: string;
  running_version?: string;
  memory_size?: number;
  timeout?: number;
  environment?: Record<string, string>;
  create_time?: string;
  update_time?: string;
  last_heartbeat?: string;
}

// 获取云节点列表请求参数
export interface GetNodeListRequest {
  node_id?: string;
  cloud_account_id?: string;
  namespace?: string;
  region?: string;
  node_type?: string;
  tag?: string;
  status?: string;
  page?: number;
  page_size?: number;
}

// 创建云节点请求参数
export interface CreateNodeRequest {
  node_name: string;
  cloud_account_id: string;
  region: string;
  namespace: string;
  function_name: string;
  runtime: string;
  package_id?: string; 
  memory_size?: number;
  timeout?: number;
  environment?: Record<string, string>;
  description?: string;
}

// 更新云节点请求参数
export interface UpdateNodeRequest {
  node_id: string;
  package_id?: string; 
  memory_size?: number;
  timeout?: number;
  environment?: Record<string, string>;
  description?: string;
}

// 批量创建云节点请求参数
export interface BatchCreateNodesRequest {
  cloud_account_id: string;
  region: string;
  namespace: string;
  function_name_prefix: string;
  runtime: string;
  package_id?: string; 
  count: number;
  memory_size?: number;
  timeout?: number;
  environment?: Record<string, string>;
}

// 批量部署云节点请求参数
export interface BatchDeployNodesRequest {
  node_ids: string[];
  package_id: string; 
}

// 批量删除云节点请求参数
export interface BatchDeleteNodesRequest {
  node_ids: string[];
}

// 获取云节点列表
export const getNodeList = async (params: GetNodeListRequest = {}): Promise<any> => {
  const response = await api.post('/cloudnode/GetNodeList', params);
  return response.data;
};

// 获取云节点详情
export const getNodeDetail = async (nodeId: string): Promise<any> => {
  const response = await api.post('/cloudnode/GetNodeDetail', {
    node_id: nodeId
  });
  return response.data;
};

// 创建云节点
export const createNode = async (data: CreateNodeRequest) => {
  const response = await api.post('/cloudnode/CreateSCFNode', data);
  return response;
};

// 更新云节点
export const updateNode = async (data: UpdateNodeRequest) => {
  const response = await api.post('/cloudnode/UpdateSCFNode', data);
  return response;
};

// 删除云节点
export const deleteNode = async (nodeId: string) => {
  const response = await api.post('/cloudnode/DeleteSCFNode', {
    node_id: nodeId
  });
  return response;
};

// 批量创建云节点
export const batchCreateNodes = async (data: BatchCreateNodesRequest) => {
  const response = await api.post('/cloudnode/BatchCreateSCFNodes', data);
  return response;
};

// 批量部署云节点
export const batchDeployNodes = async (data: BatchDeployNodesRequest) => {
  const response = await api.post('/cloudnode/BatchDeploySCFNodes', data);
  return response;
};

// 批量删除云节点
export const batchDeleteNodes = async (data: BatchDeleteNodesRequest) => {
  const response = await api.post('/cloudnode/BatchDeleteSCFNodes', data);
  return response;
};

// 节点心跳
export const nodeHeartbeat = async (nodeId: string) => {
  const response = await api.post('/cloudnode/Heartbeat', {
    node_id: nodeId
  });
  return response;
};

// 更新节点负载
export const updateNodeLoad = async (nodeId: string, loadLevel: number) => {
  const response = await api.post('/cloudnode/UpdateNodeLoad', {
    node_id: nodeId,
    load_level: loadLevel
  });
  return response;
};

// 更新节点函数信息
export const updateNodeFunction = async (nodeId: string, packageId: string) => {
  const response = await api.post('/cloudnode/UpdateNodeFunction', {
    node_id: nodeId,
    package_id: packageId
  });
  return response;
};

// 节点状态枚举
export const NODE_STATUS = {
  OFFLINE: 0,
  ONLINE: 1,
  MAINTENANCE: 2,
  OVERLOAD: 3,
} as const;

// 节点类型枚举
export const NODE_TYPE = {
  SCF: 'scf',
  SERVER: 'server',
} as const;

// 获取状态文本
export const getStatusText = (status: string | number): string => {
  if (typeof status === 'string' && status) {
    if (status === 'online') {
      return '在线';
    }
    if (status === 'offline') {
      return '离线';
    }
    return status;
  }
  const statusMap: Record<number, string> = {
    0: '离线',
    1: '在线',
    2: '维护中',
    3: '过载',
  };
  if (typeof status === 'number') {
    return statusMap[status] || '未知';
  }
  return '未知';
};

// 获取节点类型文本
export const getNodeTypeText = (type: string): string => {
  const typeMap: Record<string, string> = {
    scf: '云函数',
    server: '服务器',
  };
  return typeMap[type] || '未知';
};
