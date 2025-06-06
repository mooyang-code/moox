import { api, AUTH_INFO } from './config';

// 项目列表的类型定义
export interface Dataset {
  dataset_id: number;
  dataset_name: string;
  data_type: number;
  time_series_period: string;
  validation_rule: string;
  remark: string;
}

export interface Project {
  id: number;
  name: string;
  name_cn: string;  // 项目中文名
  remark: string;
  create_time: string;
  datasets: Dataset[];
}

// ListProjects响应类型定义
export interface ListProjectsResponse {
  code: number;
  message: string;
  projects: Project[];
}

// 获取项目列表
export const listProjects = async (): Promise<Project[]> => {
  try {
    const response = await api.post('/trpc.storage.metadata.MetaAdmin/ListProjects', {
      auth_info: AUTH_INFO
    });
    
    console.log('ListProjects API响应:', response);
    
    // 现在 response.data 直接是精简后的协议数据
    const data = response.data as ListProjectsResponse;
    console.log('协议数据:', data);
    
    // 检查协议级别的错误（非0表示错误）
    if (data.code !== 0) {
      throw new Error(data.message || '获取项目列表失败');
    }
    
    return data.projects || [];
  } catch (error: any) {
    console.error('ListProjects Error:', error);
    
    let errorMessage = '获取项目列表失败';
    
    if (error.response?.data?.message) {
      errorMessage = error.response.data.message;
    } else if (error.message) {
      errorMessage = error.message;
    }
    
    throw new Error(errorMessage);
  }
}; 