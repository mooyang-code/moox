/**
 * 存储设备常量测试
 */

import {
  EnumDeviceType,
  getDeviceTypeName,
  getDeviceTypeColor,
  getDeviceTypeDescription,
  DEVICE_TYPE_OPTIONS
} from '../storage-device';

describe('存储设备常量测试', () => {
  test('设备类型枚举值应该正确', () => {
    expect(EnumDeviceType.DUCKDB_DEVICE).toBe(2);
    expect(EnumDeviceType.BLEVE_DEVICE).toBe(3);
    expect(EnumDeviceType.CSV_DEVICE).toBe(4);
  });

  test('getDeviceTypeName 应该返回正确的设备类型名称', () => {
    expect(getDeviceTypeName(2)).toBe('DuckDB');
    expect(getDeviceTypeName(3)).toBe('Bleve');
    expect(getDeviceTypeName(4)).toBe('CSV');
    expect(getDeviceTypeName(999)).toBe('未知');
  });

  test('getDeviceTypeColor 应该返回正确的颜色', () => {
    expect(getDeviceTypeColor(2)).toBe('green');
    expect(getDeviceTypeColor(3)).toBe('magenta');
    expect(getDeviceTypeColor(4)).toBe('orange');
    expect(getDeviceTypeColor(999)).toBe('gray');
  });

  test('getDeviceTypeDescription 应该返回正确的描述', () => {
    expect(getDeviceTypeDescription(2)).toBe('DuckDB数据库，分析型数据库');
    expect(getDeviceTypeDescription(3)).toBe('Bleve全文搜索引擎');
    expect(getDeviceTypeDescription(4)).toBe('CSV文件存储');
    expect(getDeviceTypeDescription(999)).toBe('');
  });

  test('DEVICE_TYPE_OPTIONS 应该包含所有设备类型', () => {
    expect(DEVICE_TYPE_OPTIONS).toHaveLength(3);
    expect(DEVICE_TYPE_OPTIONS).toEqual([
      { value: 2, label: 'DuckDB' },
      { value: 3, label: 'Bleve' },
      { value: 4, label: 'CSV' },
    ]);
  });


});
