import service from '@/api/index';

// 字段格式类型定义
export interface FieldFormatType {
  field_primary_format: number;
  field_secondary_format: number;
}

// 字段详细信息接口
export interface FieldDetailInfo {
  proj_id: number;
  dataset_ids: number[];
  field_id: number;
  field_name: string;
  field_category: number;
  interface_name: string;  // 字段英文名
  desc: string;  // 字段描述
  required_flag: number;  // 必填标记（-1非必填；1必填）
  unique_flag: number;    // 唯一约束标记（-1否；1是）
  metadata_flag: number;  // 元数据字段标记（-1否；1是）
  parent_field_id: number;
  field_format_type: FieldFormatType;
  value_lib_id: number;
  validation_rule?: any;
  write_example: string;
  remark: string;
  ctime: string;
  mtime: string;
  invalid: number;
}

// 认证信息
export interface AuthInfo {
  app_id: string;
  app_key: string;
}

// 搜索字段请求参数
export interface SearchFieldReq {
  auth_info: AuthInfo;
  proj_id: number;
  dataset_id?: number;
  field_name?: string;
  interface_name?: string;
  field_category?: number;
  field_ids?: number[];
  page_info?: {
    page_idx: number;  // 页数(从1开始计数)
    size: number;      // 页大小(默认50，最大200)
  };
}

// 返回信息
export interface RetInfo {
  code: number;
  msg: string;
}

// 搜索字段响应
export interface SearchFieldRsp {
  ret_info: RetInfo;
  field_detail_infos: FieldDetailInfo[];
  cur_page: number;
  total_page: number;
  total_num: number;
}

// 创建字段请求参数
export interface CreateFieldReq {
  auth_info: AuthInfo;
  operator?: string;
  field_detail_info: {
    proj_id: number;
    dataset_ids?: number[];  // repeated int32 in proto
    field_name: string;
    field_category: number;
    interface_name: string;
    desc: string;
    required_flag: number;   // 必填标记（-1非必填；1必填）
    unique_flag: number;     // 唯一约束标记（-1否；1是）
    metadata_flag?: number;  // 元数据字段标记（-1否；1是）
    parent_field_id?: number;  // 父字段ID
    field_format_type: FieldFormatType;
    value_lib_id?: number;  // 属性值库ID
    validation_rule?: any;
    write_example?: string;
    remark?: string;
    // 注意：创建时不需要传递以下字段（后端会自动生成）
    // field_id, ctime, mtime, invalid
  };
}

// 更新字段请求参数
export interface UpdateFieldReq {
  auth_info: AuthInfo;
  proj_id: number;
  field_id: number;
  operator?: string;
  field_update_info: FieldDetailInfo;
}

// 删除字段请求参数
export interface DeleteFieldReq {
  auth_info: AuthInfo;
  proj_id: number;
  field_id: number;
}

// UpsertField 请求参数
export interface UpsertFieldReq {
  auth_info: AuthInfo;
  proj_id: number;
  interface_name: string;  // 字段英文名（项目维度下唯一）
  operator: string;        // 操作人
  field_detail_info: {
    proj_id: number;
    dataset_ids: number[];
    field_name: string;
    field_category: number;
    interface_name: string;
    desc: string;
    required_flag: number;   // 必填标记（-1非必填；1必填）
    unique_flag: number;     // 唯一约束标记（-1否；1是）
    metadata_flag?: number;  // 元数据字段标记（-1否；1是）
    parent_field_id?: number;
    field_format_type: FieldFormatType;
    value_lib_id?: number;
    validation_rule?: any;
    write_example?: string;
    remark?: string;
  };
}

// UpsertField 响应
export interface UpsertFieldRsp {
  ret_info: RetInfo;
  field_id: number;
}

/**
 * 搜索字段列表
 */
export const searchFields = async (params: SearchFieldReq): Promise<SearchFieldRsp> => {
  const response = await service.post('/gateway/field/SearchField', params);
  // 注意：由于响应拦截器的处理，service直接返回数据而不是response对象
  const data = response as any;
  
  // 添加data的null检查
  if (!data) {
    throw new Error('搜索字段失败：响应数据为空');
  }
  
  if (!data.ret_info) {
    throw new Error('搜索字段失败：响应格式错误，缺少ret_info字段');
  }
  
  // 检查ret_info.code是否为0（成功）
  if (data.ret_info.code !== 0) {
    throw new Error(data.ret_info.msg || '搜索字段失败');
  }
  
  return data;
};

/**
 * 创建字段
 */
export const createField = async (params: CreateFieldReq): Promise<{ ret_info: RetInfo; field_id: number }> => {
  const response = await service.post('/gateway/field/CreateField', params);
  // 注意：由于响应拦截器的处理，service直接返回数据而不是response对象
  const data = response as any;
  
  // 添加data的null检查
  if (!data) {
    throw new Error('创建字段失败：响应数据为空');
  }
  
  if (!data.ret_info) {
    throw new Error('创建字段失败：响应格式错误，缺少ret_info字段');
  }
  
  // 直接返回数据，让调用方根据ret_info.code判断成功与否
  return data;
};

/**
 * 更新字段
 */
export const updateField = async (params: UpdateFieldReq): Promise<void> => {
  const response = await service.post('/gateway/field/UpdateField', params);
  // 注意：由于响应拦截器的处理，service直接返回数据而不是response对象
  const data = response as any;
  
  // 添加data的null检查
  if (!data) {
    throw new Error('更新字段失败：响应数据为空');
  }
  
  if (!data.ret_info) {
    throw new Error('更新字段失败：响应格式错误，缺少ret_info字段');
  }
  
  // 检查ret_info.code是否为0（成功）
  if (data.ret_info.code !== 0) {
    throw new Error(data.ret_info.msg || '更新字段失败');
  }
};

/**
 * 删除字段
 */
export const deleteField = async (params: DeleteFieldReq): Promise<{ ret_info: RetInfo }> => {
  const response = await service.post('/gateway/field/DeleteField', params);
  // 注意：由于响应拦截器的处理，service直接返回数据而不是response对象
  const data = response as any;
  
  // 添加data的null检查
  if (!data) {
    throw new Error('删除字段失败：响应数据为空');
  }
  
  if (!data.ret_info) {
    throw new Error('删除字段失败：响应格式错误，缺少ret_info字段');
  }
  
  // 直接返回数据，让调用方根据ret_info.code判断成功与否
  return data;
};

/**
 * 获取字段详情
 */
export const getField = async (params: { 
  auth_info: AuthInfo;
  proj_id: number; 
  field_id: number; 
  dataset_id?: number;
}): Promise<{
  field_detail_info: FieldDetailInfo;
  field_values: any[];
}> => {
  const response = await service.post('/gateway/field/GetField', params);
  // 注意：由于响应拦截器的处理，service直接返回数据而不是response对象
  const data = response as any;
  
  // 添加data的null检查
  if (!data) {
    throw new Error('获取字段详情失败：响应数据为空');
  }
  
  if (!data.ret_info) {
    throw new Error('获取字段详情失败：响应格式错误，缺少ret_info字段');
  }
  
  // 检查ret_info.code是否为0（成功）
  if (data.ret_info.code !== 0) {
    throw new Error(data.ret_info.msg || '获取字段详情失败');
  }
  
  return data;
};

/**
 * UpsertField 创建或更新字段
 */
export const upsertField = async (params: UpsertFieldReq): Promise<UpsertFieldRsp> => {
  const response = await service.post('/gateway/field/UpsertField', params);
  // 注意：由于响应拦截器的处理，service直接返回数据而不是response对象
  const data = response as any;
  
  // 添加data的null检查
  if (!data) {
    throw new Error('UpsertField失败：响应数据为空');
  }
  
  if (!data.ret_info) {
    throw new Error('UpsertField失败：响应格式错误，缺少ret_info字段');
  }
  
  // 直接返回数据，让调用方根据ret_info.code判断成功与否
  return data;
}; 
