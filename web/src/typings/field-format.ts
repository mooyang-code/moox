/**
 * 字段格式相关类型定义
 */

// 字段二级格式枚举
export enum EnumFieldSecondaryFormat {
  UNDEFINED2 = 0,
  // 文本类型
  TEXT = 1,
  // 布尔类型 格式: true/false
  BOOLEAN = 2,
  // 日期 格式: 2021-02-03
  DATE = 3,
  // 日期范围 格式: 2021-02-03 ~ 2022-03-02
  DATE_RANGE = 4,
  // 日期时间 格式：2021-02-03 08:00:00
  DATE_TIME = 5,
  // 日期时间范围 格式: 2021-02-03 08:00:00 ~ 2022-03-02 09:00:01
  DATE_TIME_RANGE = 6,
  // 秒级时间戳 格式：1661411887
  TIMESTAMP = 7,
  // ISO8601格式的日期（例如：2025-04-12T20:36:00+08:00）
  DATE_ISO8601 = 8,
  // 链接 格式：http://puui.qpic.cn/emuczz1543346158
  URI = 9,
  // JSON
  JSON = 10,
  // 选项值ID
  OPTION_VALUE = 11,
  // 选项值中文文案
  OPTION_NAME = 12,
}

// 字段二级格式选项配置
export interface FieldSecondaryFormatOption {
  value: string;
  name: string;
  description?: string;
}

// 字段二级格式选项列表
export const FIELD_SECONDARY_FORMAT_OPTIONS: FieldSecondaryFormatOption[] = [
  { value: "1", name: "文本类型", description: "纯文本格式" },
  { value: "2", name: "布尔类型", description: "格式: true/false" },
  { value: "3", name: "日期", description: "格式: 2021-02-03" },
  { value: "4", name: "日期范围", description: "格式: 2021-02-03 ~ 2022-03-02" },
  { value: "5", name: "日期时间", description: "格式: 2021-02-03 08:00:00" },
  { value: "6", name: "日期时间范围", description: "格式: 2021-02-03 08:00:00 ~ 2022-03-02 09:00:01" },
  { value: "7", name: "秒级时间戳", description: "格式: 1661411887" },
  { value: "8", name: "ISO8601日期", description: "格式: 2025-04-12T20:36:00+08:00" },
  { value: "9", name: "链接", description: "格式: http://puui.qpic.cn/emuczz1543346158" },
  { value: "10", name: "JSON", description: "JSON格式数据" },
  { value: "11", name: "选项值ID", description: "选项值的ID标识" },
  { value: "12", name: "选项值中文文案", description: "选项值的中文显示文案" },
];

// 获取字段二级格式名称
export function getFieldSecondaryFormatName(value: string | number): string {
  const option = FIELD_SECONDARY_FORMAT_OPTIONS.find(item => item.value === String(value));
  return option ? option.name : "未知格式";
}

// 获取字段二级格式描述
export function getFieldSecondaryFormatDescription(value: string | number): string {
  const option = FIELD_SECONDARY_FORMAT_OPTIONS.find(item => item.value === String(value));
  return option ? (option.description || "") : "";
} 