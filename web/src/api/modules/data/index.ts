import axios from "@/api";
import { api } from "@/api/config";

// QueryObject相关接口类型定义
export interface AuthInfo {
  app_id: string;
  app_key: string;
}

export interface PageInfo {
  page_idx: number;  // 页数(从1开始计数)
  size: number;      // 页大小
}

export interface Options {
  includes?: string[];     // 返回字段
  max_num?: number;       // 搜索结果的最大数量（默认10000）
}

export interface QueryObjectReq {
  auth_info: AuthInfo;
  project_id: number;
  dataset_id: number;
  options?: Options;
  page_info?: PageInfo;
}

export interface SimpleValue {
  // 根据protobuf定义，实际字段名为str而不是string_value
  str?: string;
  int?: number;
  float?: number;
  time?: string;
  // 保留兼容性字段
  int_value?: number;
  double_value?: number;
  string_value?: string;
  bool_value?: boolean;
}

export interface MapContainer {
  map_data?: Record<string, SimpleValue>;
}

export interface FieldValue {
  field_key: string;
  field_category: number;
  simple_value?: SimpleValue;
  map_value?: MapContainer;
  option_mapping?: Record<number, string>;
}

export interface ObjectRow {
  object_id: string;
  object_name?: string; // 对象名称，可选
  fields: Record<string, FieldValue>;
}

export interface RetInfo {
  code: number;
  msg: string;
}

export interface QueryObjectRsp {
  ret_info: RetInfo;
  total: number;
  object_rows: ObjectRow[];
  failed_fields?: Record<string, any>;
}

// UpsertObject相关接口
export interface UpdateField {
  field_key: string;
  field_category: number;
  update_type: number; // 1=SET_UPDATE, 2=DEL_UPDATE, 3=APPEND_UPDATE
  simple_value: SimpleValue;
  map_value?: any;
}

export interface UpdateObjectRow {
  object_id: string;
  fields: Record<string, UpdateField>;
}

export interface UpsertObjectReq {
  auth_info: AuthInfo;
  project_id: number;
  dataset_id: number;
  object_rows: UpdateObjectRow[];
}

export interface FailedObjectRow {
  object_id: string;
  failed_list: Record<string, any>;
}

export interface UpsertObjectRsp {
  ret_info: RetInfo;
  failed_rows?: FailedObjectRow[];
}

// FetchObject相关接口
export interface FetchObjectReq {
  auth_info: AuthInfo;
  project_id: number;
  dataset_id: number;
  field_keys?: string[];
  map_keys?: Record<string, any>;
}

export interface FetchObjectRsp {
  ret_info: RetInfo;
  object_rows: ObjectRow[];
  failed_fields?: Record<string, any>;
}

// DeleteObject相关接口
export interface DeleteObjectReq {
  auth_info: AuthInfo;
  project_id: number;
  dataset_id: number;
  object_ids: string[];  // 要删除的数据对象ID列表（必填，精确删除指定对象）
}

export interface DeleteObjectRsp {
  ret_info: RetInfo;
}

// 认证信息配置
const AUTH_INFO: AuthInfo = {
  app_id: 'moox_frontend',
  app_key: '2521e0d21b6be0347b72bca93904a0dd'
};

// QueryObject接口
export const queryObjectAPI = async (params: {
  project_id: number;
  dataset_id: number;
  page_info?: PageInfo;
  options?: Options;
}): Promise<QueryObjectRsp> => {
  try {
    const requestData: QueryObjectReq = {
      auth_info: AUTH_INFO,
      ...params
    };

    const response = await api.post('/storage/QueryObject', requestData);

    // 使用api实例，需要访问response.data
    const data = response.data;

    // 添加响应数据的安全检查
    if (!data) {
      throw new Error('QueryObject接口调用失败：响应数据为空');
    }

    // 检查是否有ret_info字段
    if (!data.ret_info) {
      console.error('QueryObject响应缺少ret_info字段:', data);
      throw new Error('QueryObject接口调用失败：响应格式错误，缺少ret_info字段');
    }

    return data as QueryObjectRsp;
  } catch (error: any) {
    console.error('QueryObject API调用失败:', error);
    throw new Error(error.message || 'QueryObject接口调用失败');
  }
};

// UpsertObject接口 - 创建或更新数据对象
export const upsertObjectAPI = async (params: {
  project_id: number;
  dataset_id: number;
  object_rows: UpdateObjectRow[];
}): Promise<UpsertObjectRsp> => {
  try {
    const requestData: UpsertObjectReq = {
      auth_info: AUTH_INFO,
      ...params
    };

    const response = await api.post('/storage/UpsertObject', requestData);
    const data = response.data;

    if (!data) {
      throw new Error('UpsertObject接口调用失败：响应数据为空');
    }

    if (!data.ret_info) {
      console.error('UpsertObject响应缺少ret_info字段:', data);
      throw new Error('UpsertObject接口调用失败：响应格式错误，缺少ret_info字段');
    }

    return data as UpsertObjectRsp;
  } catch (error: any) {
    console.error('UpsertObject API调用失败:', error);
    throw new Error(error.message || 'UpsertObject接口调用失败');
  }
};

// FetchObject接口 - 获取数据对象详情
export const fetchObjectAPI = async (params: {
  project_id: number;
  dataset_id: number;
  field_keys?: string[];
  map_keys?: Record<string, any>;
}): Promise<FetchObjectRsp> => {
  try {
    const requestData: FetchObjectReq = {
      auth_info: AUTH_INFO,
      ...params
    };

    const response = await api.post('/storage/FetchObject', requestData);
    const data = response.data;

    if (!data) {
      throw new Error('FetchObject接口调用失败：响应数据为空');
    }

    if (!data.ret_info) {
      console.error('FetchObject响应缺少ret_info字段:', data);
      throw new Error('FetchObject接口调用失败：响应格式错误，缺少ret_info字段');
    }

    return data as FetchObjectRsp;
  } catch (error: any) {
    console.error('FetchObject API调用失败:', error);
    throw new Error(error.message || 'FetchObject接口调用失败');
  }
};

// DeleteObject接口 - 删除数据对象
export const deleteObjectAPI = async (params: {
  project_id: number;
  dataset_id: number;
  object_ids: string[];
}): Promise<DeleteObjectRsp> => {
  try {
    // 验证必须提供object_ids
    if (!params.object_ids || params.object_ids.length === 0) {
      throw new Error('删除操作失败：必须提供要删除的对象ID列表');
    }

    const requestData: DeleteObjectReq = {
      auth_info: AUTH_INFO,
      ...params
    };

    const response = await api.post('/storage/DeleteObject', requestData);
    const data = response.data;

    if (!data) {
      throw new Error('DeleteObject接口调用失败：响应数据为空');
    }

    if (!data.ret_info) {
      console.error('DeleteObject响应缺少ret_info字段:', data);
      throw new Error('DeleteObject接口调用失败：响应格式错误，缺少ret_info字段');
    }

    return data as DeleteObjectRsp;
  } catch (error: any) {
    console.error('DeleteObject API调用失败:', error);
    throw new Error(error.message || 'DeleteObject接口调用失败');
  }
};

// 获取对象列表数据（保留兼容性）
export const getObjectListAPI = () => {
  return axios({
    url: "/mock/data/object-list",
    method: "get"
  });
};

// 获取数据列表数据
export const getDataListAPI = () => {
  return axios({
    url: "/mock/data/data-list",
    method: "get"
  });
};

// 根据项目ID获取对象列表
export const getObjectListByProjectAPI = (projectId: string) => {
  return axios({
    url: `/mock/data/project/${projectId}/object-list`,
    method: "get"
  });
};

// 根据项目ID获取数据列表
export const getDataListByProjectAPI = (projectId: string) => {
  return axios({
    url: `/mock/data/project/${projectId}/data-list`,
    method: "get"
  });
};

// SearchData相关接口类型定义（基于access.proto）
export interface DataKey {
  project_id: number;
  dataset_id: number;
  object_id: string;
  freq?: string; // 时序频率，可选
}

export interface TimeRange {
  start: string; // 格式: "YYYY-MM-DD HH:MM:SS"
  end?: string;   // 格式: "YYYY-MM-DD HH:MM:SS"
  periods?: number; // 周期数
}

export interface SortInfo {
  field_name: string;
  sort_type: number; // 1=升序, 2=降序
}

export interface Cond {
  field_name: string;
  op: number;        // 操作符
  value: any;        // 条件值
  map_key?: string;  // map字段的key
}

export interface CondGroup {
  conds: Cond[];
  logical: number;   // 条件间关系
}

export interface SearchOptions {
  cond_groups?: CondGroup[];  // 请求条件组
  logical?: number;           // 条件组关系
  expr_cond?: string;         // 表达式请求条件
  sort?: SortInfo[];          // 排序信息
  includes?: string[];        // 返回字段
  max_num?: number;          // 搜索结果的最大数量（默认10000）
}

export interface SearchDataReq {
  auth_info: AuthInfo;
  data_key: DataKey;
  time_range?: TimeRange;
  time_sort?: number;     // 时序排序类型：0=升序，1=降序
  row_id?: string;        // 行ID，用于静态数据查询
  options?: SearchOptions;
  page_info?: PageInfo;
}

export interface FieldValue {
  field_key: string;
  field_category: number;
  simple_value?: SimpleValue;
  map_value?: MapContainer;
}

export interface DataRow {
  times?: string;     // 时序数据的时刻值
  row_id: string;     // 数据行ID
  fields: Record<string, FieldValue>; // 字段值列表
}

export interface SearchDataRsp {
  ret_info: RetInfo;
  total: number;
  data_rows: DataRow[];
  failed_fields?: Record<string, any>;
}

// SearchData接口 - 搜索数据
export const searchDataAPI = async (params: {
  data_key: DataKey;
  time_range?: TimeRange;
  time_sort?: number;
  row_id?: string;
  options?: SearchOptions;
  page_info?: PageInfo;
}): Promise<SearchDataRsp> => {
  try {
    const requestData: SearchDataReq = {
      auth_info: AUTH_INFO,
      ...params
    };

    const response = await api.post('/storage/SearchData', requestData);
    const data = response.data;

    if (!data) {
      throw new Error('SearchData接口调用失败：响应数据为空');
    }

    if (!data.ret_info) {
      console.error('SearchData响应缺少ret_info字段:', data);
      throw new Error('SearchData接口调用失败：响应格式错误，缺少ret_info字段');
    }

    // 检查ret_info.code是否为0（成功）
    if (data.ret_info.code !== 0) {
      throw new Error(data.ret_info.msg || 'SearchData接口调用失败');
    }

    return data as SearchDataRsp;
  } catch (error: any) {
    console.error('SearchData API调用失败:', error);
    throw new Error(error.message || 'SearchData接口调用失败');
  }
};
