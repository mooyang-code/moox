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
  
  // 文件信息
  original_filename?: string;
  file_size: number;
  file_md5: string;
  
  // COS存储信息
  cloud_account_id: string;
  cos_region: string;
  cos_bucket: string;
  cos_path: string;
  cos_url?: string;
  
  // 状态管理
  status: number;
  status_label: string;
  upload_progress?: number;
  error_message?: string;
  
  // 使用统计
  last_deploy_time?: string;
  
  // 审计字段
  created_by: string;
  invalid?: number;
  created_at: string;
  updated_at?: string;
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


// 获取代码包下载URL（新方法）
export const getPackageDownloadURL = async (id: number): Promise<any> => {
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

// 简单的URL下载（新的推荐方式）
export const downloadPackageByURL = async (id: number): Promise<void> => {
  try {
    console.log(`开始获取代码包 ${id} 的下载URL...`);
    
    // 1. 获取下载URL
    const response = await getPackageDownloadURL(id);
    
    if (response?.code !== 200 || !response?.data?.[0]) {
      throw new Error('获取下载URL失败');
    }
    
    const downloadInfo = response.data[0];
    const { download_url, filename } = downloadInfo;
    
    console.log(`获取到下载URL: ${download_url}, 文件名: ${filename}`);
    
    // 2. 构建完整的下载URL
    // 从浏览器当前URL中提取IP（冒号之前的部分）
    const currentURL = window.location.href;
    const urlObj = new URL(currentURL);
    const hostname = urlObj.hostname; // 获取IP地址部分
    
    // 构建文件服务器URL（端口18080）
    const fullDownloadURL = `http://${hostname}:18080${download_url}`;
    
    console.log(`构建的完整下载URL: ${fullDownloadURL}`);
    
    // 3. 创建隐藏的下载链接
    const link = document.createElement('a');
    link.href = fullDownloadURL;
    link.download = filename;
    link.style.display = 'none';
    
    // 4. 触发下载
    document.body.appendChild(link);
    link.click();
    document.body.removeChild(link);
    
    console.log(`✓ 下载已触发: ${filename}`);
    
  } catch (error) {
    console.error('URL下载失败:', error);
    throw error;
  }
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