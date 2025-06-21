import { api, AUTH_INFO } from './config';

// 数据集接口相关类型定义
export interface CreateDatasetRequest {
  proj_id: number;
  dataset_name: string;
  data_type: number; // 1: 静态数据, 2: 时序数据
  freqs?: string; // 时序周期，如 "1m+5m+1H+1D"
  check_rules?: string; // 校验规则
  comment?: string; // 备注
}

export interface UpdateDatasetRequest {
  proj_id: number;
  dataset_id: number;
  dataset_name?: string;
  check_rules?: string;
  comment?: string;
}

export interface DeleteDatasetRequest {
  proj_id: number;
  dataset_id: number;
}

// 返回信息类型定义
export interface RetInfo {
  code: number;
  msg: string;
}

// 数据集响应类型定义
export interface DatasetResponse {
  ret_info: RetInfo;
  dataset_id?: number;
}

// 创建数据集
export const createDataset = async (params: CreateDatasetRequest): Promise<DatasetResponse> => {
  try {
    console.log('创建数据集请求参数:', params);
    
    const response = await api.post('/metadata/CreateDataSet', {
      auth_info: {
        app_id: AUTH_INFO.app_id,
        app_key: AUTH_INFO.app_key
      },
      ...params
    });

    console.log('创建数据集响应:', response.data);
    const data = response?.data;
    
    // 添加安全检查
    if (!data) {
      throw new Error('创建数据集失败：响应数据为空');
    }
    
    if (!data.ret_info) {
      throw new Error('创建数据集失败：响应格式错误，缺少ret_info字段');
    }
    
    // 检查ret_info.code是否为0（成功）
    if (data.ret_info.code !== 0) {
      throw new Error(data.ret_info.msg || '创建数据集失败');
    }
    
    return data;
  } catch (error: any) {
    console.error('创建数据集失败:', error);
    throw new Error(error?.message || '创建数据集失败');
  }
};

// 更新数据集
export const updateDataset = async (params: UpdateDatasetRequest): Promise<DatasetResponse> => {
  try {
    console.log('更新数据集请求参数:', params);
    
    const response = await api.post('/metadata/UpdateDataSet', {
      auth_info: {
        app_id: AUTH_INFO.app_id,
        app_key: AUTH_INFO.app_key
      },
      ...params
    });

    console.log('更新数据集响应:', response.data);
    const data = response?.data;
    
    // 添加安全检查
    if (!data) {
      throw new Error('更新数据集失败：响应数据为空');
    }
    
    if (!data.ret_info) {
      throw new Error('更新数据集失败：响应格式错误，缺少ret_info字段');
    }
    
    // 检查ret_info.code是否为0（成功）
    if (data.ret_info.code !== 0) {
      throw new Error(data.ret_info.msg || '更新数据集失败');
    }
    
    return data;
  } catch (error: any) {
    console.error('更新数据集失败:', error);
    throw new Error(error?.message || '更新数据集失败');
  }
};

// 删除数据集
export const deleteDataset = async (params: DeleteDatasetRequest): Promise<DatasetResponse> => {
  try {
    console.log('删除数据集请求参数:', params);
    
    const response = await api.post('/metadata/DeleteDataSet', {
      auth_info: {
        app_id: AUTH_INFO.app_id,
        app_key: AUTH_INFO.app_key
      },
      ...params
    });

    console.log('删除数据集响应:', response.data);
    const data = response?.data;
    
    // 添加安全检查
    if (!data) {
      throw new Error('删除数据集失败：响应数据为空');
    }
    
    if (!data.ret_info) {
      throw new Error('删除数据集失败：响应格式错误，缺少ret_info字段');
    }
    
    // 检查ret_info.code是否为0（成功）
    if (data.ret_info.code !== 0) {
      throw new Error(data.ret_info.msg || '删除数据集失败');
    }
    
    return data;
  } catch (error: any) {
    console.error('删除数据集失败:', error);
    throw new Error(error?.message || '删除数据集失败');
  }
}; 