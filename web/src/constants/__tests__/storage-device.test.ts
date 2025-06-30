/**
 * 存储设备常量测试
 */

import {
  EnumDeviceType,
  EnumSchemaRequired,
  getDeviceTypeName,
  getDeviceTypeColor,
  getDeviceSchemaRequired,
  getDeviceTypeDescription,
  DEVICE_TYPE_OPTIONS,
  SCHEMA_REQUIRED_OPTIONS
} from '../storage-device';

describe('存储设备常量测试', () => {
  test('设备类型枚举值应该正确', () => {
    expect(EnumDeviceType.SQLITE_DEVICE).toBe(1);
    expect(EnumDeviceType.DUCKDB_DEVICE).toBe(2);
    expect(EnumDeviceType.BLEVE_DEVICE).toBe(3);
    expect(EnumDeviceType.CSV_DEVICE).toBe(4);
  });

  test('Schema需求枚举值应该正确', () => {
    expect(EnumSchemaRequired.NO_SCHEMA).toBe(-1);
    expect(EnumSchemaRequired.NEED_SCHEMA).toBe(1);
  });

  test('getDeviceTypeName 应该返回正确的设备类型名称', () => {
    expect(getDeviceTypeName(1)).toBe('SQLite');
    expect(getDeviceTypeName(2)).toBe('DuckDB');
    expect(getDeviceTypeName(3)).toBe('Bleve');
    expect(getDeviceTypeName(4)).toBe('CSV');
    expect(getDeviceTypeName(999)).toBe('未知');
  });

  test('getDeviceTypeColor 应该返回正确的颜色', () => {
    expect(getDeviceTypeColor(1)).toBe('blue');
    expect(getDeviceTypeColor(2)).toBe('green');
    expect(getDeviceTypeColor(3)).toBe('magenta');
    expect(getDeviceTypeColor(4)).toBe('orange');
    expect(getDeviceTypeColor(999)).toBe('gray');
  });

  test('getDeviceSchemaRequired 应该返回正确的Schema需求', () => {
    expect(getDeviceSchemaRequired(1)).toBe(1); // SQLite需要Schema
    expect(getDeviceSchemaRequired(2)).toBe(1); // DuckDB需要Schema
    expect(getDeviceSchemaRequired(3)).toBe(-1); // Bleve无需Schema
    expect(getDeviceSchemaRequired(4)).toBe(1); // CSV需要Schema
    expect(getDeviceSchemaRequired(999)).toBe(1); // 未知类型默认需要Schema
  });

  test('getDeviceTypeDescription 应该返回正确的描述', () => {
    expect(getDeviceTypeDescription(1)).toBe('SQLite数据库，轻量级关系型数据库');
    expect(getDeviceTypeDescription(2)).toBe('DuckDB数据库，分析型数据库');
    expect(getDeviceTypeDescription(3)).toBe('Bleve全文搜索引擎');
    expect(getDeviceTypeDescription(4)).toBe('CSV文件存储');
    expect(getDeviceTypeDescription(999)).toBe('');
  });

  test('DEVICE_TYPE_OPTIONS 应该包含所有设备类型', () => {
    expect(DEVICE_TYPE_OPTIONS).toHaveLength(4);
    expect(DEVICE_TYPE_OPTIONS).toEqual([
      { value: 1, label: 'SQLite' },
      { value: 2, label: 'DuckDB' },
      { value: 3, label: 'Bleve' },
      { value: 4, label: 'CSV' },
    ]);
  });

  test('SCHEMA_REQUIRED_OPTIONS 应该包含所有Schema选项', () => {
    expect(SCHEMA_REQUIRED_OPTIONS).toHaveLength(2);
    expect(SCHEMA_REQUIRED_OPTIONS).toEqual([
      { value: 1, label: '需要Schema（SQLite、DuckDB、CSV）' },
      { value: -1, label: '无需Schema（Bleve）' },
    ]);
  });
});
