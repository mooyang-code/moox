// 数据对象行接口（基于QueryObjectRsp中的ObjectRow）
export interface ObjectRow {
  object_id: string;
  fields: Record<string, FieldValue>;
}

// 字段值接口（基于access.proto中的FieldValue）
export interface FieldValue {
  field_key: string;
  field_type: number;
  simple_value?: SimpleValue;
  map_value?: MapContainer;
  option_mapping?: Record<number, string>;
}

// 简单值接口（根据实际API返回结构调整）
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

// Map容器接口
export interface MapContainer {
  map_data?: Record<string, SimpleValue>;
}

// 字段详情信息（用于字段名翻译）
export interface FieldDetailInfo {
  field_id: number;
  field_name: string;
  field_name_en: string;
  data_category: number;
}

// 搜索表单数据
export interface FormData {
  form: {
    [key: string]: string; // 动态字段
  };
  search: boolean;
}

export interface RowSelection {
  type: string;
  showCheckedAll: boolean;
  onlyCurrent: boolean;
}

export interface Pagination {
  showPageSize: boolean;
  showTotal: boolean;
  current: number;
  pageSize: number;
  total: number;
  pageSizeOptions?: number[];
}
