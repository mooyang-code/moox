import { api } from '@/api/config';
import { isRetInfoSuccess } from '@/api/ret-info';
export { withOptionalSpace } from '@/api/space-context';

// 云节点接口定义（对应后端 pb.CloudNode）
export interface CloudNode {
  id?: number;
  node_id: string;
  node_type: string;
  cloud_account_id: string;
  region: string;
  namespace: string;
  function_name?: string;
  runtime?: string;
  handler?: string;
  status: string;        // 后端计算得出的状态文本：online/offline/timeout/abnormal
  status_desc?: string;
  load_level?: number;
  package_id?: string;
  package_name?: string;
  package_version?: string;
  running_version?: string;
  memory_size?: number;
  timeout?: number;
  config?: Record<string, string>;
  environment?: Record<string, string>;
  biz_type?: string;
  tag?: string;
  ip_address?: string;
  supported_collectors?: string;
  metadata?: string;
  timeout_threshold?: number;
  heartbeat_interval?: number;
  probe_enabled?: boolean;
  probe_url?: string;
  last_heartbeat?: string;
  invalid?: number;
  create_time?: string;
  modify_time?: string;
}

// 获取云节点列表请求参数（对应 pb.NodeListRequest）
export interface GetNodeListRequest {
  node_id?: string;
  cloud_account_id?: string;
  namespace?: string;
  region?: string;
  node_type?: string;
  biz_type?: string;
  tag?: string;
  status?: string;
  page?: number;
  page_size?: number;
}

// 创建云节点请求参数
export interface CreateNodeRequest {
  node_name?: string;
  cloud_account_id: string;
  region: string;
  namespace?: string;
  function_name?: string;
  runtime: string;
  handler?: string;
  package_id?: string;
  memory_size?: number;
  timeout?: number;
  config?: Record<string, string>;
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
  handler?: string;
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

// 统一校验 ret_info 并提取业务数据；失败抛错。
function unwrap<T = any>(rsp: any): T {
  if (rsp?.ret_info && !isRetInfoSuccess(rsp.ret_info.code)) {
    throw new Error(rsp.ret_info.msg || '请求失败');
  }
  return rsp as T;
}

// 获取云节点列表
export const getNodeList = async (params: GetNodeListRequest = {}): Promise<{ items: CloudNode[]; total: number }> => {
  const response = await api.post('/cloudnode/GetNodeList', { query: params });
  const rsp = unwrap<{ items?: CloudNode[]; total?: number }>(response.data);
  return { items: rsp.items ?? [], total: rsp.total ?? 0 };
};

// 获取云节点详情
export const getNodeDetail = async (nodeId: string): Promise<CloudNode | null> => {
  const response = await api.post('/cloudnode/GetNodeDetail', { node_id: nodeId });
  const rsp = unwrap<{ node?: CloudNode }>(response.data);
  return rsp.node ?? null;
};

// 更新云节点
export const updateNode = async (data: UpdateNodeRequest): Promise<void> => {
  const response = await api.post('/cloudnode/UpdateNode', { node: data });
  unwrap(response.data);
};

// 删除云节点
export const deleteNode = async (nodeId: string): Promise<void> => {
  const response = await api.post('/cloudnode/DeleteNode', { node_id: nodeId });
  unwrap(response.data);
};

// 批量创建云节点
export const batchCreateNodes = async (data: BatchCreateNodesRequest): Promise<{ job_id: string; total_task_cnt: number }> => {
  // 将前端扁平结构转为后端 NodeCreateItem 列表
  const nodes = Array.from({ length: data.count }).map(() => ({
    cloud_account_id: data.cloud_account_id,
    region: data.region,
    namespace: data.namespace,
    runtime: data.runtime,
    handler: data.handler,
    package_id: data.package_id,
    environment: data.environment,
  }));
  const response = await api.post('/cloudnode/BatchCreateNodes', { nodes });
  const rsp = unwrap<{ job_id?: string; total_task_cnt?: number }>(response.data);
  return { job_id: rsp.job_id ?? '', total_task_cnt: rsp.total_task_cnt ?? 0 };
};

// 批量部署云节点
export const batchDeployNodes = async (data: BatchDeployNodesRequest): Promise<{ job_id: string; total_task_cnt: number }> => {
  const deployments = data.node_ids.map(id => ({ node_id: id, package_id: data.package_id }));
  const response = await api.post('/cloudnode/BatchDeployNodes', { deployments });
  const rsp = unwrap<{ job_id?: string; total_task_cnt?: number }>(response.data);
  return { job_id: rsp.job_id ?? '', total_task_cnt: rsp.total_task_cnt ?? 0 };
};

// 批量删除云节点
export const batchDeleteNodes = async (data: BatchDeleteNodesRequest): Promise<{ job_id: string; total_task_cnt: number }> => {
  const response = await api.post('/cloudnode/BatchDeleteNodes', { node_ids: data.node_ids });
  const rsp = unwrap<{ job_id?: string; total_task_cnt?: number }>(response.data);
  return { job_id: rsp.job_id ?? '', total_task_cnt: rsp.total_task_cnt ?? 0 };
};

// 节点心跳
export const nodeHeartbeat = async (nodeId: string): Promise<void> => {
  const response = await api.post('/cloudnode/ReportHeartbeat', { node_id: nodeId });
  unwrap(response.data);
};

// 更新节点负载（走 UpdateNode）
export const updateNodeLoad = async (nodeId: string, loadLevel: number): Promise<void> => {
  const response = await api.post('/cloudnode/UpdateNode', { node: { node_id: nodeId, load_level: loadLevel } });
  unwrap(response.data);
};

// 更新节点函数信息（代码包）
export const updateNodeFunction = async (nodeId: string, packageId: string): Promise<void> => {
  const response = await api.post('/cloudnode/UpdateNodeFunction', { node_id: nodeId, package_id: packageId });
  unwrap(response.data);
};

// 节点状态枚举（字符串，与后端 calcNodeStatusText 对应）
export const NODE_STATUS = {
  OFFLINE: 'offline',
  ONLINE: 'online',
  TIMEOUT: 'timeout',
  ABNORMAL: 'abnormal',
} as const;

// 节点类型枚举
export const NODE_TYPE = {
  SCF: 'scf',
  SERVER: 'server',
} as const;

// 获取状态文本
export const getStatusText = (status: string | number): string => {
  if (typeof status === 'string' && status) {
    const map: Record<string, string> = {
      online: '在线',
      offline: '离线',
      timeout: '超时',
      abnormal: '异常',
    };
    return map[status] || status;
  }
  // 兼容历史数值状态
  const numMap: Record<number, string> = {
    0: '离线',
    1: '在线',
    2: '超时',
    3: '异常',
  };
  if (typeof status === 'number') {
    return numMap[status] || '未知';
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
