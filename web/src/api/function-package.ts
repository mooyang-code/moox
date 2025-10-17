import { api } from '@/api/config';

// 云函数代码包接口定义
export interface FunctionPackage {
  id: number;
  package_name: string;
  version: string;
  description: string;
  runtime: string;
  package_type: string;
  package_type_label: string;
  file_size: number;
  status: number;
  status_label: string;
  last_deploy_time?: string;
  created_by: string;
  created_at: string;
}

// 上传代码包请求
export interface UploadPackageRequest {
  package_name: string;
  version: string;
  description?: string;
  runtime: string;
  package_type: string;
  file_content: string; // base64编码的文件内容
  cloud_account_id?: string; // 云账户ID，可选
}

// 代码包列表请求
export interface PackageListRequest {
  page?: number;
  page_size?: number;
  package_name?: string;
  runtime?: string;
  package_type?: string;
  status?: number;
}

// 代码包列表响应
export interface PackageListResponse {
  total: number;
  items: FunctionPackage[];
}

// 代码包选项
export interface PackageOption {
  id: number;
  label: string;
  package_name: string;
  version: string;
  runtime: string;
  package_type: string;
}

// 异步上传响应
export interface UploadPackageAsyncResponse {
  task_id: string;
  package_id: number;
  package_name: string;
  version: string;
  status: number;
  is_async: boolean;
}

// 上传任务状态响应
export interface UploadTaskStatusResponse {
  task_id: string;
  status: string; // pending, processing, success, failed
  progress: number;
  message: string;
  is_complete: boolean;
}

// 上传云函数代码包（异步）
export const uploadFunctionPackage = async (data: UploadPackageRequest) => {
  const response = await api.post('/collector/UploadPackage', data);
  return response;
};

// 获取云函数代码包列表
export const getFunctionPackageList = async (params: PackageListRequest = {}): Promise<any> => {
  const response = await api.post('/collector/GetPackageList', params);
  return response.data;
};

// 获取云函数代码包详情
export const getFunctionPackageDetail = async (id: number): Promise<any> => {
  const response = await api.post('/collector/GetPackageDetail', { id });
  return response.data;
};

// 删除云函数代码包
export const deleteFunctionPackage = async (id: number) => {
  const response = await api.post('/collector/DeletePackage', { id });
  return response;
};

// 获取云函数代码包下载链接
export const getFunctionPackageDownloadURL = async (id: number): Promise<any> => {
  const response = await api.post('/collector/GetPackageDownloadURL', { id });
  return response.data;
};

// 获取代码包选项（用于下拉选择）
export const getFunctionPackageOptions = async (packageType?: string): Promise<any> => {
  const params = packageType ? { package_type: packageType } : {};
  const response = await api.post('/collector/GetPackageOptions', params);
  return response.data;
};

// 获取上传任务状态
export const getUploadTaskStatus = async (taskId: string): Promise<any> => {
  const response = await api.post('/collector/GetUploadTaskStatus', { task_id: taskId });
  return response.data;
};

// 下载本地存储的代码包
export const downloadLocalPackage = (id: number) => {
  // 对于本地下载，需要直接使用API路径而不是网关路径
  const host = window.location.hostname;
  const port = 20103; // 后端服务端口
  window.open(`http://${host}:${port}/api/function-packages/${id}/download-local`, '_blank');
};

// 运行时环境选项
export const RUNTIME_OPTIONS = [
  { label: 'Go1', value: 'Go1' },
  { label: 'Python 3.7', value: 'Python3.7' },
  { label: 'Python 3.9', value: 'Python3.9' },
  { label: 'Node.js 14.18', value: 'Nodejs14.18' },
  { label: 'Node.js 16.13', value: 'Nodejs16.13' }
];

// 函数包类型选项
export const PACKAGE_TYPE_OPTIONS = [
  { label: '数据采集类型', value: 'data_collector' },
  { label: '因子计算类型', value: 'factor_calculator' }
];

// 状态选项
export const STATUS_OPTIONS = [
  { label: '上传中', value: 0 },
  { label: '可用', value: 1 },
  { label: '已删除', value: 2 },
  { label: '上传失败', value: 3 }
];