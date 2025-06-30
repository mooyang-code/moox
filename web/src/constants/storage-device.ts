/**
 * 存储设备相关常量定义
 */

// 存储设备类型枚举
export enum EnumDeviceType {
  // SQLite 存储设备
  SQLITE_DEVICE = 1,
  // DuckDB 存储设备
  DUCKDB_DEVICE = 2,
  // Bleve 存储设备
  BLEVE_DEVICE = 3,
  // CSV 存储设备
  CSV_DEVICE = 4,
}

// Schema需求枚举
export enum EnumSchemaRequired {
  // 无需Schema
  NO_SCHEMA = -1,
  // 需要Schema
  NEED_SCHEMA = 1,
}

// 设备类型配置
export const DEVICE_TYPE_CONFIG = {
  [EnumDeviceType.SQLITE_DEVICE]: {
    name: 'SQLite',
    color: 'blue',
    schemaRequired: EnumSchemaRequired.NEED_SCHEMA,
    description: 'SQLite数据库，轻量级关系型数据库'
  },
  [EnumDeviceType.DUCKDB_DEVICE]: {
    name: 'DuckDB',
    color: 'green',
    schemaRequired: EnumSchemaRequired.NEED_SCHEMA,
    description: 'DuckDB数据库，分析型数据库'
  },
  [EnumDeviceType.BLEVE_DEVICE]: {
    name: 'Bleve',
    color: 'magenta',
    schemaRequired: EnumSchemaRequired.NO_SCHEMA,
    description: 'Bleve全文搜索引擎'
  },
  [EnumDeviceType.CSV_DEVICE]: {
    name: 'CSV',
    color: 'orange',
    schemaRequired: EnumSchemaRequired.NEED_SCHEMA,
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

// 获取设备类型的Schema需求
export const getDeviceSchemaRequired = (type: number): number => {
  const config = DEVICE_TYPE_CONFIG[type as EnumDeviceType];
  return config?.schemaRequired || EnumSchemaRequired.NEED_SCHEMA;
};

// 获取设备类型描述
export const getDeviceTypeDescription = (type: number): string => {
  const config = DEVICE_TYPE_CONFIG[type as EnumDeviceType];
  return config?.description || '';
};

// 设备类型选项列表
export const DEVICE_TYPE_OPTIONS = [
  { value: EnumDeviceType.SQLITE_DEVICE, label: 'SQLite' },
  { value: EnumDeviceType.DUCKDB_DEVICE, label: 'DuckDB' },
  { value: EnumDeviceType.BLEVE_DEVICE, label: 'Bleve' },
  { value: EnumDeviceType.CSV_DEVICE, label: 'CSV' },
];

// Schema需求选项列表
export const SCHEMA_REQUIRED_OPTIONS = [
  { value: EnumSchemaRequired.NEED_SCHEMA, label: '需要Schema（SQLite、DuckDB、CSV）' },
  { value: EnumSchemaRequired.NO_SCHEMA, label: '无需Schema（Bleve）' },
];
