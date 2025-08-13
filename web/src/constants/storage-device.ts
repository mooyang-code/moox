/**
 * 存储设备相关常量定义
 */

// 存储设备类型枚举
export enum EnumDeviceType {
  // DuckDB 存储设备
  DUCKDB_DEVICE = 2,
  // Bleve 存储设备
  BLEVE_DEVICE = 3,
  // CSV 存储设备
  CSV_DEVICE = 4,
}



// 设备类型配置
export const DEVICE_TYPE_CONFIG = {
  [EnumDeviceType.DUCKDB_DEVICE]: {
    name: 'DuckDB',
    color: 'green',
    description: 'DuckDB数据库，分析型数据库'
  },
  [EnumDeviceType.BLEVE_DEVICE]: {
    name: 'Bleve',
    color: 'magenta',
    description: 'Bleve全文搜索引擎'
  },
  [EnumDeviceType.CSV_DEVICE]: {
    name: 'CSV',
    color: 'orange',
    description: 'CSV文件存储'
  },
} as const;

// 获取设备类型名称
export const getDeviceTypeName = (type: number): string => {
  const config = DEVICE_TYPE_CONFIG[type as EnumDeviceType];
  return config?.name || '未知';
};

// 获取设备类型颜色
export const getDeviceTypeColor = (type: number): string => {
  const config = DEVICE_TYPE_CONFIG[type as EnumDeviceType];
  return config?.color || 'gray';
};



// 获取设备类型描述
export const getDeviceTypeDescription = (type: number): string => {
  const config = DEVICE_TYPE_CONFIG[type as EnumDeviceType];
  return config?.description || '';
};

// 设备类型选项列表
export const DEVICE_TYPE_OPTIONS = [
  { value: EnumDeviceType.DUCKDB_DEVICE, label: 'DuckDB' },
  { value: EnumDeviceType.BLEVE_DEVICE, label: 'Bleve' },
  { value: EnumDeviceType.CSV_DEVICE, label: 'CSV' },
];


