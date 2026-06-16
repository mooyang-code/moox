import { api, AUTH_INFO } from './config';

// ================================================================================
// 存储节点相关类型定义

export interface StorageNode {
  node_id: number;
  node_alias: string;
  node_srv_conn: string;
  ctime: string;
  mtime: string;
  enabled: string; // 是否启用（"true"=启用；"false"=禁用）
}

export interface ListStorageNodesResponse {
  ret_info: {
    code: number;
    msg: string;
  };
  nodes: StorageNode[];
}

// ================================================================================
// 存储设备相关类型定义

export interface StorageDevice {
  device_id: number;
  device_name: string;
  device_type: number;
  conn_info: string;
  ctime: string;
  mtime: string;
  enabled: string; // 是否启用（"true"=启用；"false"=禁用）
}

export interface ListStorageDevicesResponse {
  ret_info: {
    code: number;
    msg: string;
  };
  devices: StorageDevice[];
}

// ================================================================================
// 数据对象路由相关类型定义

export interface ObjectRoute {
  route_id: number;
  dataset_id: number;
  object_id: string;
  node_id: number;
  ctime: string;
  mtime: string;
  enabled: string; // 是否启用（"true"=启用；"false"=禁用）
}

export interface ListObjectRoutesRequest {
  project_id: number;
  dataset_id?: number;
  node_id?: number;
  page_info?: {
    page_no: number;
    page_size: number;
  };
}

export interface ListObjectRoutesResponse {
  ret_info: {
    code: number;
    msg: string;
  };
  routes: ObjectRoute[];
}

// ================================================================================
// 数据字段路由相关类型定义

export interface FieldRoute {
  route_id: number;
  field_id: number; // 字段ID，使用999999999表示所有字段
  dataset_id: number; // 数据集ID，为0表示该项目下所有的数据集
  device_id: number;
  ctime: string;
  mtime: string;
  enabled: string; // 是否启用（"true"=启用；"false"=禁用）
}

export interface ListFieldRoutesRequest {
  project_id: number;
  field_id?: number; // 字段ID过滤条件，使用999999999表示所有字段
  dataset_id?: number; // 数据集ID过滤条件，为0表示该项目下所有的数据集
  device_id?: number;
  data_category?: string; // 数据类别过滤条件
  page_info?: {
    page_no: number;
    page_size: number;
  };
}

// 字段路由常量
export const FIELD_ROUTE_CONSTANTS = {
  ALL_FIELDS_MARKER: 999999999, // 表示所有字段
  ALL_DATASETS_MARKER: 0, // 表示所有数据集
} as const;

export interface ListFieldRoutesResponse {
  ret_info: {
    code: number;
    msg: string;
  };
  routes: FieldRoute[];
}

// ================================================================================
// API接口函数

// 获取存储节点列表
export const listStorageNodes = async (): Promise<ListStorageNodesResponse> => {
  try {
    console.log('获取存储节点列表');
    
    const response = await api.post('/metadata/ListStorageNodes', {
      auth_info: {
        app_id: AUTH_INFO.app_id,
        app_key: AUTH_INFO.app_key
      }
    });

    console.log('存储节点列表响应:', response.data);
    const data = response?.data;
    
    if (!data) {
      throw new Error('获取存储节点列表失败：响应数据为空');
    }
    
    if (!data.ret_info) {
      throw new Error('获取存储节点列表失败：响应格式错误，缺少ret_info字段');
    }
    
    if (data.ret_info.code !== 0) {
      throw new Error(data.ret_info.msg || '获取存储节点列表失败');
    }
    
    return data;
  } catch (error: any) {
    console.error('获取存储节点列表失败:', error);
    throw new Error(error?.message || '获取存储节点列表失败');
  }
};

// 获取存储设备列表
export const listStorageDevices = async (): Promise<ListStorageDevicesResponse> => {
  try {
    console.log('获取存储设备列表');
    
    const response = await api.post('/metadata/ListStorageDevices', {
      auth_info: {
        app_id: AUTH_INFO.app_id,
        app_key: AUTH_INFO.app_key
      }
    });

    console.log('存储设备列表响应:', response.data);
    const data = response?.data;
    
    if (!data) {
      throw new Error('获取存储设备列表失败：响应数据为空');
    }
    
    if (!data.ret_info) {
      throw new Error('获取存储设备列表失败：响应格式错误，缺少ret_info字段');
    }
    
    if (data.ret_info.code !== 0) {
      throw new Error(data.ret_info.msg || '获取存储设备列表失败');
    }
    
    return data;
  } catch (error: any) {
    console.error('获取存储设备列表失败:', error);
    throw new Error(error?.message || '获取存储设备列表失败');
  }
};

// 获取数据对象路由列表
export const listObjectRoutes = async (params: ListObjectRoutesRequest): Promise<ListObjectRoutesResponse> => {
  try {
    console.log('获取数据对象路由列表', params);

    const requestData: any = {
      auth_info: {
        app_id: AUTH_INFO.app_id,
        app_key: AUTH_INFO.app_key
      },
      project_id: params.project_id
    };

    // 添加搜索参数
    if (params?.dataset_id) {
      requestData.dataset_id = params.dataset_id;
    }
    if (params?.node_id) {
      requestData.node_id = params.node_id;
    }
    if (params?.page_info) {
      requestData.page_info = params.page_info;
    }
    
    const response = await api.post('/metadata/ListObjectRoutes', requestData);

    console.log('数据对象路由列表响应:', response.data);
    const data = response?.data;
    
    if (!data) {
      throw new Error('获取数据对象路由列表失败：响应数据为空');
    }
    
    if (!data.ret_info) {
      throw new Error('获取数据对象路由列表失败：响应格式错误，缺少ret_info字段');
    }
    
    if (data.ret_info.code !== 0) {
      throw new Error(data.ret_info.msg || '获取数据对象路由列表失败');
    }
    
    return data;
  } catch (error: any) {
    console.error('获取数据对象路由列表失败:', error);
    throw new Error(error?.message || '获取数据对象路由列表失败');
  }
};

// 获取数据字段路由列表
export const listFieldRoutes = async (params: ListFieldRoutesRequest): Promise<ListFieldRoutesResponse> => {
  try {
    console.log('获取数据字段路由列表', params);

    const requestData: any = {
      auth_info: {
        app_id: AUTH_INFO.app_id,
        app_key: AUTH_INFO.app_key
      },
      project_id: params.project_id
    };

    // 添加搜索参数
    if (params?.field_id) {
      requestData.field_id = params.field_id;
    }
    if (params?.data_category) {
      requestData.data_category = params.data_category;
    }
    if (params?.device_id) {
      requestData.device_id = params.device_id;
    }
    if (params?.page_info) {
      requestData.page_info = params.page_info;
    }
    
    const response = await api.post('/metadata/ListFieldRoutes', requestData);

    console.log('数据字段路由列表响应:', response.data);
    const data = response?.data;
    
    if (!data) {
      throw new Error('获取数据字段路由列表失败：响应数据为空');
    }
    
    if (!data.ret_info) {
      throw new Error('获取数据字段路由列表失败：响应格式错误，缺少ret_info字段');
    }
    
    if (data.ret_info.code !== 0) {
      throw new Error(data.ret_info.msg || '获取数据字段路由列表失败');
    }
    
    return data;
  } catch (error: any) {
    console.error('获取数据字段路由列表失败:', error);
    throw new Error(error?.message || '获取数据字段路由列表失败');
  }
};

// ================================================================================
// 创建、更新、删除存储节点相关接口

export interface CreateStorageNodeRequest {
  node_alias: string;
  node_srv_conn: string;
}

export interface CreateStorageNodeResponse {
  ret_info: {
    code: number;
    msg: string;
  };
  node_id?: number;
}

export interface UpdateStorageNodeRequest {
  node_id: number;
  node_alias: string;
}

export interface UpdateStorageNodeResponse {
  ret_info: {
    code: number;
    msg: string;
  };
}

export interface DeleteStorageNodeRequest {
  node_id: number;
}

export interface DeleteStorageNodeResponse {
  ret_info: {
    code: number;
    msg: string;
  };
}

// 创建存储节点
export const createStorageNode = async (params: CreateStorageNodeRequest): Promise<CreateStorageNodeResponse> => {
  try {
    console.log('创建存储节点', params);
    
    const response = await api.post('/metadata/CreateStorageNode', {
      auth_info: {
        app_id: AUTH_INFO.app_id,
        app_key: AUTH_INFO.app_key
      },
      ...params
    });

    console.log('创建存储节点响应:', response.data);
    const data = response?.data;
    
    if (!data) {
      throw new Error('创建存储节点失败：响应数据为空');
    }
    
    if (!data.ret_info) {
      throw new Error('创建存储节点失败：响应格式错误，缺少ret_info字段');
    }
    
    if (data.ret_info.code !== 0) {
      throw new Error(data.ret_info.msg || '创建存储节点失败');
    }
    
    return data;
  } catch (error: any) {
    console.error('创建存储节点失败:', error);
    throw new Error(error?.message || '创建存储节点失败');
  }
};

// 更新存储节点
export const updateStorageNode = async (params: UpdateStorageNodeRequest): Promise<UpdateStorageNodeResponse> => {
  try {
    console.log('更新存储节点', params);
    
    const response = await api.post('/metadata/UpdateStorageNode', {
      auth_info: {
        app_id: AUTH_INFO.app_id,
        app_key: AUTH_INFO.app_key
      },
      node_id: params.node_id,
      node_alias: params.node_alias
    });

    console.log('更新存储节点响应:', response.data);
    const data = response?.data;
    
    if (!data) {
      throw new Error('更新存储节点失败：响应数据为空');
    }
    
    if (!data.ret_info) {
      throw new Error('更新存储节点失败：响应格式错误，缺少ret_info字段');
    }
    
    if (data.ret_info.code !== 0) {
      throw new Error(data.ret_info.msg || '更新存储节点失败');
    }
    
    return data;
  } catch (error: any) {
    console.error('更新存储节点失败:', error);
    throw new Error(error?.message || '更新存储节点失败');
  }
};

// 删除存储节点
export const deleteStorageNode = async (params: DeleteStorageNodeRequest): Promise<DeleteStorageNodeResponse> => {
  try {
    console.log('删除存储节点', params);
    
    const response = await api.post('/metadata/DeleteStorageNode', {
      auth_info: {
        app_id: AUTH_INFO.app_id,
        app_key: AUTH_INFO.app_key
      },
      ...params
    });

    console.log('删除存储节点响应:', response.data);
    const data = response?.data;
    
    if (!data) {
      throw new Error('删除存储节点失败：响应数据为空');
    }
    
    if (!data.ret_info) {
      throw new Error('删除存储节点失败：响应格式错误，缺少ret_info字段');
    }
    
    if (data.ret_info.code !== 0) {
      throw new Error(data.ret_info.msg || '删除存储节点失败');
    }
    
    return data;
  } catch (error: any) {
    console.error('删除存储节点失败:', error);
    throw new Error(error?.message || '删除存储节点失败');
  }
};

// ================================================================================
// 创建、更新、删除存储设备相关接口

export interface CreateStorageDeviceRequest {
  device_name: string;
  device_type: number;
  conn_info: string;
}

export interface CreateStorageDeviceResponse {
  ret_info: {
    code: number;
    msg: string;
  };
  device_id?: number;
}

export interface UpdateStorageDeviceRequest {
  device_id: number;
  device_name: string;
}

export interface UpdateStorageDeviceResponse {
  ret_info: {
    code: number;
    msg: string;
  };
}

export interface DeleteStorageDeviceRequest {
  device_id: number;
}

export interface DeleteStorageDeviceResponse {
  ret_info: {
    code: number;
    msg: string;
  };
}

// 创建存储设备
export const createStorageDevice = async (params: CreateStorageDeviceRequest): Promise<CreateStorageDeviceResponse> => {
  try {
    console.log('创建存储设备', params);
    
    const response = await api.post('/metadata/CreateStorageDevice', {
      auth_info: {
        app_id: AUTH_INFO.app_id,
        app_key: AUTH_INFO.app_key
      },
      ...params
    });

    console.log('创建存储设备响应:', response.data);
    const data = response?.data;
    
    if (!data) {
      throw new Error('创建存储设备失败：响应数据为空');
    }
    
    if (!data.ret_info) {
      throw new Error('创建存储设备失败：响应格式错误，缺少ret_info字段');
    }
    
    if (data.ret_info.code !== 0) {
      throw new Error(data.ret_info.msg || '创建存储设备失败');
    }
    
    return data;
  } catch (error: any) {
    console.error('创建存储设备失败:', error);
    throw new Error(error?.message || '创建存储设备失败');
  }
};

// 更新存储设备
export const updateStorageDevice = async (params: UpdateStorageDeviceRequest): Promise<UpdateStorageDeviceResponse> => {
  try {
    console.log('更新存储设备', params);
    
    const response = await api.post('/metadata/UpdateStorageDevice', {
      auth_info: {
        app_id: AUTH_INFO.app_id,
        app_key: AUTH_INFO.app_key
      },
      device_id: params.device_id,
      device_name: params.device_name
    });

    console.log('更新存储设备响应:', response.data);
    const data = response?.data;
    
    if (!data) {
      throw new Error('更新存储设备失败：响应数据为空');
    }
    
    if (!data.ret_info) {
      throw new Error('更新存储设备失败：响应格式错误，缺少ret_info字段');
    }
    
    if (data.ret_info.code !== 0) {
      throw new Error(data.ret_info.msg || '更新存储设备失败');
    }
    
    return data;
  } catch (error: any) {
    console.error('更新存储设备失败:', error);
    throw new Error(error?.message || '更新存储设备失败');
  }
};

// 删除存储设备
export const deleteStorageDevice = async (params: DeleteStorageDeviceRequest): Promise<DeleteStorageDeviceResponse> => {
  try {
    console.log('删除存储设备', params);
    
    const response = await api.post('/metadata/DeleteStorageDevice', {
      auth_info: {
        app_id: AUTH_INFO.app_id,
        app_key: AUTH_INFO.app_key
      },
      ...params
    });

    console.log('删除存储设备响应:', response.data);
    const data = response?.data;
    
    if (!data) {
      throw new Error('删除存储设备失败：响应数据为空');
    }
    
    if (!data.ret_info) {
      throw new Error('删除存储设备失败：响应格式错误，缺少ret_info字段');
    }
    
    if (data.ret_info.code !== 0) {
      throw new Error(data.ret_info.msg || '删除存储设备失败');
    }
    
    return data;
  } catch (error: any) {
    console.error('删除存储设备失败:', error);
    throw new Error(error?.message || '删除存储设备失败');
  }
};

// ================================================================================
// 创建、更新、删除数据对象路由相关接口

export interface CreateObjectRouteRequest {
  project_id: number;
  dataset_id: number;
  object_id: string;
  node_id: number;
}

export interface CreateObjectRouteResponse {
  ret_info: {
    code: number;
    msg: string;
  };
  route_id?: number;
}

export interface UpdateObjectRouteRequest {
  route_id: number;
  dataset_id: number;
  object_id: string;
  node_id: number;
}

export interface UpdateObjectRouteResponse {
  ret_info: {
    code: number;
    msg: string;
  };
}

export interface DeleteObjectRouteRequest {
  route_id: number;
}

export interface DeleteObjectRouteResponse {
  ret_info: {
    code: number;
    msg: string;
  };
}

// 创建数据对象路由
export const createObjectRoute = async (params: CreateObjectRouteRequest): Promise<CreateObjectRouteResponse> => {
  try {
    console.log('创建数据对象路由', params);
    
    const response = await api.post('/metadata/CreateObjectRoute', {
      auth_info: {
        app_id: AUTH_INFO.app_id,
        app_key: AUTH_INFO.app_key
      },
      ...params
    });

    console.log('创建数据对象路由响应:', response.data);
    const data = response?.data;
    
    if (!data) {
      throw new Error('创建数据对象路由失败：响应数据为空');
    }
    
    if (!data.ret_info) {
      throw new Error('创建数据对象路由失败：响应格式错误，缺少ret_info字段');
    }
    
    if (data.ret_info.code !== 0) {
      throw new Error(data.ret_info.msg || '创建数据对象路由失败');
    }
    
    return data;
  } catch (error: any) {
    console.error('创建数据对象路由失败:', error);
    throw new Error(error?.message || '创建数据对象路由失败');
  }
};

// 更新数据对象路由
export const updateObjectRoute = async (params: UpdateObjectRouteRequest): Promise<UpdateObjectRouteResponse> => {
  try {
    console.log('更新数据对象路由', params);
    
    const response = await api.post('/metadata/UpdateObjectRoute', {
      auth_info: {
        app_id: AUTH_INFO.app_id,
        app_key: AUTH_INFO.app_key
      },
      route_id: params.route_id,
      dataset_id: params.dataset_id,
      object_id: params.object_id,
      node_id: params.node_id
    });

    console.log('更新数据对象路由响应:', response.data);
    const data = response?.data;
    
    if (!data) {
      throw new Error('更新数据对象路由失败：响应数据为空');
    }
    
    if (!data.ret_info) {
      throw new Error('更新数据对象路由失败：响应格式错误，缺少ret_info字段');
    }
    
    if (data.ret_info.code !== 0) {
      throw new Error(data.ret_info.msg || '更新数据对象路由失败');
    }
    
    return data;
  } catch (error: any) {
    console.error('更新数据对象路由失败:', error);
    throw new Error(error?.message || '更新数据对象路由失败');
  }
};

// 删除数据对象路由
export const deleteObjectRoute = async (params: DeleteObjectRouteRequest): Promise<DeleteObjectRouteResponse> => {
  try {
    console.log('删除数据对象路由', params);
    
    const response = await api.post('/metadata/DeleteObjectRoute', {
      auth_info: {
        app_id: AUTH_INFO.app_id,
        app_key: AUTH_INFO.app_key
      },
      route_id: params.route_id
    });

    console.log('删除数据对象路由响应:', response.data);
    const data = response?.data;
    
    if (!data) {
      throw new Error('删除数据对象路由失败：响应数据为空');
    }
    
    if (!data.ret_info) {
      throw new Error('删除数据对象路由失败：响应格式错误，缺少ret_info字段');
    }
    
    if (data.ret_info.code !== 0) {
      throw new Error(data.ret_info.msg || '删除数据对象路由失败');
    }
    
    return data;
  } catch (error: any) {
    console.error('删除数据对象路由失败:', error);
    throw new Error(error?.message || '删除数据对象路由失败');
  }
};

// ================================================================================
// 创建、更新、删除数据字段路由相关接口

export interface CreateFieldRouteRequest {
  project_id: number;
  field_id: number;
  dataset_id?: number;
  device_id: number;
}

export interface CreateFieldRouteResponse {
  ret_info: {
    code: number;
    msg: string;
  };
  route_id?: number;
}

export interface UpdateFieldRouteRequest {
  route_id: number;
  field_id: number;
  dataset_id?: number;
  device_id: number;
}

export interface UpdateFieldRouteResponse {
  ret_info: {
    code: number;
    msg: string;
  };
}

export interface DeleteFieldRouteRequest {
  route_id: number;
}

export interface DeleteFieldRouteResponse {
  ret_info: {
    code: number;
    msg: string;
  };
}

// 创建数据字段路由
export const createFieldRoute = async (params: CreateFieldRouteRequest): Promise<CreateFieldRouteResponse> => {
  try {
    console.log('创建数据字段路由', params);
    
    const response = await api.post('/metadata/CreateFieldRoute', {
      auth_info: {
        app_id: AUTH_INFO.app_id,
        app_key: AUTH_INFO.app_key
      },
      ...params
    });

    console.log('创建数据字段路由响应:', response.data);
    const data = response?.data;
    
    if (!data) {
      throw new Error('创建数据字段路由失败：响应数据为空');
    }
    
    if (!data.ret_info) {
      throw new Error('创建数据字段路由失败：响应格式错误，缺少ret_info字段');
    }
    
    if (data.ret_info.code !== 0) {
      throw new Error(data.ret_info.msg || '创建数据字段路由失败');
    }
    
    return data;
  } catch (error: any) {
    console.error('创建数据字段路由失败:', error);
    throw new Error(error?.message || '创建数据字段路由失败');
  }
};

// 更新数据字段路由
export const updateFieldRoute = async (params: UpdateFieldRouteRequest): Promise<UpdateFieldRouteResponse> => {
  try {
    console.log('更新数据字段路由', params);
    
    const response = await api.post('/metadata/UpdateFieldRoute', {
      auth_info: {
        app_id: AUTH_INFO.app_id,
        app_key: AUTH_INFO.app_key
      },
      route_id: params.route_id,
      field_id: params.field_id,
      device_id: params.device_id
    });

    console.log('更新数据字段路由响应:', response.data);
    const data = response?.data;
    
    if (!data) {
      throw new Error('更新数据字段路由失败：响应数据为空');
    }
    
    if (!data.ret_info) {
      throw new Error('更新数据字段路由失败：响应格式错误，缺少ret_info字段');
    }
    
    if (data.ret_info.code !== 0) {
      throw new Error(data.ret_info.msg || '更新数据字段路由失败');
    }
    
    return data;
  } catch (error: any) {
    console.error('更新数据字段路由失败:', error);
    throw new Error(error?.message || '更新数据字段路由失败');
  }
};

// 删除数据字段路由
export const deleteFieldRoute = async (params: DeleteFieldRouteRequest): Promise<DeleteFieldRouteResponse> => {
  try {
    console.log('删除数据字段路由', params);
    
    const response = await api.post('/metadata/DeleteFieldRoute', {
      auth_info: {
        app_id: AUTH_INFO.app_id,
        app_key: AUTH_INFO.app_key
      },
      route_id: params.route_id
    });

    console.log('删除数据字段路由响应:', response.data);
    const data = response?.data;
    
    if (!data) {
      throw new Error('删除数据字段路由失败：响应数据为空');
    }
    
    if (!data.ret_info) {
      throw new Error('删除数据字段路由失败：响应格式错误，缺少ret_info字段');
    }
    
    if (data.ret_info.code !== 0) {
      throw new Error(data.ret_info.msg || '删除数据字段路由失败');
    }
    
    return data;
  } catch (error: any) {
    console.error('删除数据字段路由失败:', error);
    throw new Error(error?.message || '删除数据字段路由失败');
  }
}; 
