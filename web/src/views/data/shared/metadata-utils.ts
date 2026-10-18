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
  if (status === "failed") return "red";
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
    default:
      return status || "-";
  }
}

export function formatTime(value?: string) {
  if (!value) return "-";
  return value.replace("T", " ").replace(/Z$/, "");
}

export function jsonText(value?: string) {
  return value?.trim() || "{}";
}

export function joinList(value?: string[]) {
  return (value || []).join(",");
}

const internalFieldPrefixPattern = /^dataset_[0-9a-z_]+__(.+)$/;

export function displayFieldId(fieldId: string | undefined) {
  const id = (fieldId || "").trim();
  const matched = internalFieldPrefixPattern.exec(id);
  return matched?.[1] || id;
}

export const statusOptions: SelectOption[] = [
  { label: "启用", value: "active" },
  { label: "禁用", value: "disabled" }
];

export const dataKindOptions: SelectOption<DataKind>[] = [
  { label: "记录", value: "DATA_KIND_RECORD", aliases: [1, "record"] },
  { label: "时序", value: "DATA_KIND_TIME_SERIES", aliases: [2, "time_series"] }
];

export const fieldValueTypeOptions: SelectOption<FieldValueType>[] = [
  { label: "字符串", value: "FIELD_VALUE_TYPE_STRING", aliases: [1] },
  { label: "整数", value: "FIELD_VALUE_TYPE_INT", aliases: [2] },
  { label: "浮点数", value: "FIELD_VALUE_TYPE_DOUBLE", aliases: [3] },
  { label: "布尔", value: "FIELD_VALUE_TYPE_BOOL", aliases: [4] },
  { label: "时间", value: "FIELD_VALUE_TYPE_TIME", aliases: [5] },
  { label: "JSON", value: "FIELD_VALUE_TYPE_JSON", aliases: [6] },
  { label: "二进制", value: "FIELD_VALUE_TYPE_BYTES", aliases: [7] }
];

export function optionLabel<T extends string | number>(options: SelectOption<T>[], value?: T | null) {
  if (value === undefined || value === null || value === "") return "-";
  const matched = options.find(item => item.value === value || item.aliases?.some(alias => alias === value));
  return matched?.label || String(value);
}

export function isTimeSeriesDataKind(value?: DataKind | string | number) {
  return value === "DATA_KIND_TIME_SERIES" || value === "time_series" || value === 2;
}
