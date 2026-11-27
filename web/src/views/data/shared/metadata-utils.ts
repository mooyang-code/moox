import type { DataKind, FieldValueType } from "@/api/storage/types";

export interface AdminPagination {
  current: number;
  pageSize: number;
  total: number;
  showTotal: boolean;
  showPageSize: boolean;
  pageSizeOptions: number[];
}

export interface SelectOption<T extends string | number = string> {
  label: string;
  value: T;
  aliases?: Array<string | number>;
}

export interface PageResultTotal {
  total: number;
}

export const defaultPagination = (): AdminPagination => ({
  current: 1,
  pageSize: 20,
  total: 0,
  showTotal: true,
  showPageSize: true,
  pageSizeOptions: [20, 50, 100]
});

export function pageResultTotal(page?: PageResultTotal) {
  if (!page) return 0;
  if (typeof page.total !== "number" || !Number.isFinite(page.total) || page.total < 0) {
    throw new Error(`page_result.total must be a number, got ${typeof page.total}`);
  }
  return page.total;
}

export function applyPageResult(pagination: AdminPagination, page?: PageResultTotal) {
  pagination.total = pageResultTotal(page);
}

export function statusColor(status?: string) {
  if (status === "active") return "green";
  if (status === "disabled") return "orange";
  if (status === "failed" || status === "deleted") return "red";
  if (status === "building") return "blue";
  return "gray";
}

export function statusLabel(status?: string) {
  switch (status?.toLowerCase()) {
    case "active":
    case "enabled":
      return "已启用";
    case "disabled":
    case "inactive":
      return "已停用";
    case "deleted":
      return "已删除";
    default:
      return status || "-";
  }
}

export function jsonText(value?: string) {
  return value?.trim() || "{}";
}

export const statusOptions: SelectOption[] = [
  { label: "启用", value: "active" },
  { label: "禁用", value: "disabled" }
];

export const dataKindOptions: SelectOption<DataKind>[] = [
  { label: "记录", value: "DATA_KIND_RECORD", aliases: [1, "record"] },
  { label: "时序", value: "DATA_KIND_TIME_SERIES", aliases: [2, "time_series"] }
];

export type CanonicalFieldValueType = "string" | "int" | "double" | "bool" | "time" | "json" | "bytes";

export const canonicalFieldValueTypeOptions: SelectOption<CanonicalFieldValueType>[] = [
  { label: "浮点数", value: "double" },
  { label: "整数", value: "int" },
  { label: "字符串", value: "string" },
  { label: "布尔", value: "bool" },
  { label: "时间", value: "time" },
  { label: "JSON", value: "json" },
  { label: "二进制", value: "bytes" }
];

const VALUE_TYPE_MAP: Record<string, CanonicalFieldValueType> = {
  "1": "string",
  string: "string",
  field_value_type_string: "string",
  "2": "int",
  int: "int",
  field_value_type_int: "int",
  "3": "double",
  double: "double",
  field_value_type_double: "double",
  "4": "bool",
  bool: "bool",
  field_value_type_bool: "bool",
  "5": "time",
  time: "time",
  field_value_type_time: "time",
  "6": "json",
  json: "json",
  field_value_type_json: "json",
  "7": "bytes",
  bytes: "bytes",
  field_value_type_bytes: "bytes"
};

/** 统一 Collector 小写类型名与 Storage 枚举/数字到标准值类型键。 */
export function normalizeFieldValueType(value?: FieldValueType | string | null): CanonicalFieldValueType | "" {
  if (value === undefined || value === null) return "";
  const key = String(value).trim().toLowerCase();
  return VALUE_TYPE_MAP[key] || "";
}

export function fieldValueTypeLabel(value?: FieldValueType | string | null): string {
  const normalized = normalizeFieldValueType(value);
  if (normalized) return optionLabel(canonicalFieldValueTypeOptions, normalized);
  if (value === undefined || value === null || value === "" || value === 0 || value === "FIELD_VALUE_TYPE_UNSPECIFIED") {
    return "—";
  }
  return String(value);
}

/** 缺失填充策略中文名：prev 前值填充、zero 补零。 */
export function fillStrategyLabel(fill?: string | null): string {
  const normalized = (fill || "").trim().toLowerCase();
  if (!normalized) return "—";
  if (normalized === "prev") return "前值填充";
  if (normalized === "zero") return "补零";
  return fill!.trim();
}

export function optionLabel<T extends string | number>(options: SelectOption<T>[], value?: T | null) {
  if (value === undefined || value === null || value === "") return "-";
  const matched = options.find(item => item.value === value || item.aliases?.some(alias => alias === value));
  return matched?.label || String(value);
}

export function isTimeSeriesDataKind(value?: DataKind | string | number) {
  return value === "DATA_KIND_TIME_SERIES" || value === "time_series" || value === 2;
}
