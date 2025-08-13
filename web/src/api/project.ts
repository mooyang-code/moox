import { api, AUTH_INFO } from './config';

// 项目列表的类型定义
export interface Dataset {
  dataset_id: number;
  dataset_name: string;
  data_type: number;
  time_series_period: string; // 时序周期，对应后端的time_series_period
  validation_rule: string; // 校验规则，对应后端的validation_rule
  remark: string; // 备注，对应后端的remark
}

export interface Project {
  id: number;
  name: string;
  name_cn: string;  // 项目中文名
  remark: string;
  create_time: string;
  datasets: Dataset[];
}

// 返回信息类型定义
export interface RetInfo {
  code: number;
  msg: string;
}

// ListProjects响应类型定义
export interface ListProjectsResponse {
  ret_info: RetInfo;
  projects: Project[];
}

// 获取项目列表
export const listProjects = async (): Promise<Project[]> => {
  try {
    const response = await api.post('/metadata/ListProjects', {
      auth_info: AUTH_INFO
    });
    
    console.log('ListProjects API响应:', response);
    
    const data = response?.data as ListProjectsResponse;
    console.log('协议数据:', data);
    
    // 添加安全检查
    if (!data) {
      throw new Error('获取项目列表失败：响应数据为空');
    }
    
    if (!data.ret_info) {
      throw new Error('获取项目列表失败：响应格式错误，缺少ret_info字段');
    }
    
    // 检查ret_info.code是否为0（成功）
    if (data.ret_info.code !== 0) {
      throw new Error(data.ret_info.msg || '获取项目列表失败');
    }
    
    return data.projects || [];
  } catch (error: unknown) {
    console.error('ListProjects Error:', error);

    let errorMessage = '获取项目列表失败';

    if (error && typeof error === 'object') {
      const errorObj = error as any;
      if (errorObj.response?.data?.ret_info?.msg) {
        errorMessage = errorObj.response.data.ret_info.msg;
      } else if (errorObj.message) {
        errorMessage = errorObj.message;
      }
    } else if (error instanceof Error) {
      errorMessage = error.message;
    }

    throw new Error(errorMessage);
  }
}; 
