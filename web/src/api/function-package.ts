import { api } from '@/api/config';


// 云函数代码包接口定义
export interface FunctionPackage {
  id: number;
  package_id: string; 
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
  package_id: string; 
  label: string;
  package_name: string;
  version: string;
  runtime: string;
  package_type: string;
}

// 异步上传响应（使用统一的异步任务响应格式）
export interface UploadPackageAsyncResponse {
  job_id: string;
  package_name: string;
  version: string;
  status: number;
  message: string;
}

// 上传云函数代码包（通过异步任务）
export const uploadFunctionPackage = async (data: UploadPackageRequest) => {
  // 使用统一的异步任务创建接口
  const response = await api.post('/asynctask/CreateAsyncJob', {
    tasks: [{
      task_type: 'UPLOAD_FILE_TO_COS',
      request_params: data
    }]
  });
  return response;
};

// 获取云函数代码包列表
export const getFunctionPackageList = async (params: PackageListRequest = {}): Promise<any> => {
  const response = await api.post('/cloudnode/GetPackageList', params);
  return response.data;
};

// 获取云函数代码包详情
export const getFunctionPackageDetail = async (packageId: string): Promise<any> => {
  const response = await api.post('/cloudnode/GetPackageDetail', { package_id: packageId });
  return response.data;
};

// 删除云函数代码包
export const deleteFunctionPackage = async (packageId: string) => {
  const response = await api.post('/cloudnode/DeletePackage', { package_id: packageId });
  return response;
};


// 获取代码包下载URL（新方法）
export const getPackageDownloadURL = async (packageId: string): Promise<any> => {
  const response = await api.post('/cloudnode/GetPackageDownloadURL', { package_id: packageId });
  return response.data;
};


// 获取代码包选项（用于下拉选择）
export const getFunctionPackageOptions = async (packageType?: string): Promise<any> => {
  const params = packageType ? { package_type: packageType } : {};
  const response = await api.post('/cloudnode/GetPackageOptions', params);
  return response.data;
};


// 简单的URL下载（新的推荐方式）
export const downloadPackageByURL = async (packageId: string): Promise<void> => {
  try {
    console.log(`开始获取代码包 ${packageId} 的下载URL...`);

    // 1. 获取下载URL
    const response = await getPackageDownloadURL(packageId);

    if (response?.code !== 200 || !response?.data?.[0]) {
      throw new Error('获取下载URL失败');
    }

    const downloadInfo = response.data[0];
    const { download_url, filename } = downloadInfo;

    console.log(`获取到下载URL: ${download_url}, 文件名: ${filename}`);

    // 2. 构建完整的下载URL
    const currentURL = window.location.href;
    const urlObj = new URL(currentURL);
    const hostname = urlObj.hostname;
    const fullDownloadURL = `http://${hostname}:18080${download_url}`;

    console.log(`构建的完整下载URL: ${fullDownloadURL}`);

    // 3. 使用隐藏的 <a> 标签触发下载（在当前页面弹出下载框）
    // 后端已设置 Content-Disposition: attachment，浏览器会自动下载而不是导航
    const link = document.createElement('a');
    link.href = fullDownloadURL;
    link.style.display = 'none';

    document.body.appendChild(link);
    link.click();

    // 延迟移除，确保下载已触发
    setTimeout(() => {
      document.body.removeChild(link);
    }, 100);

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
