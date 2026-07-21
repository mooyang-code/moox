export type DataKind = "DATA_KIND_UNSPECIFIED" | "DATA_KIND_RECORD" | "DATA_KIND_TIME_SERIES" | number;

export type FieldValueType =
  | "FIELD_VALUE_TYPE_UNSPECIFIED"
  | "FIELD_VALUE_TYPE_STRING"
  | "FIELD_VALUE_TYPE_INT"
  | "FIELD_VALUE_TYPE_DOUBLE"
  | "FIELD_VALUE_TYPE_BOOL"
  | "FIELD_VALUE_TYPE_TIME"
  | "FIELD_VALUE_TYPE_JSON"
  | "FIELD_VALUE_TYPE_BYTES"
  | number;

export type DatasetColumnOriginType =
  | "DATASET_COLUMN_ORIGIN_TYPE_UNSPECIFIED"
  | "DATASET_COLUMN_ORIGIN_TYPE_FIELD"
  | "DATASET_COLUMN_ORIGIN_TYPE_FACTOR"
  | "DATASET_COLUMN_ORIGIN_TYPE_SYSTEM"
  | number;

export type ColumnOriginType =
  | "COLUMN_ORIGIN_TYPE_UNSPECIFIED"
  | "COLUMN_ORIGIN_TYPE_DATASET_COLUMN"
  | "COLUMN_ORIGIN_TYPE_SYSTEM"
  | "COLUMN_ORIGIN_TYPE_EXPRESSION"
  | number;

export type SortOrder = "SORT_ORDER_ASC" | "SORT_ORDER_DESC" | number;

export type TotalMode = "AUTO" | "NONE" | "FORCE_EXACT" | number;

export type TotalState = "UNKNOWN" | "EXACT" | "SKIPPED" | number;

export interface RetInfo {
  code: number | string;
  msg: string;
}

export interface Page {
  page?: number;
  size?: number;
  cursor?: string;
}

export interface PageResult {
  page: number;
  size: number;
  total: number;
  has_more: boolean;
  next_cursor: string;
  total_state?: TotalState;
}

export interface TimeRange {
  start_time?: string;
  end_time?: string;
}

export interface VersionRange {
  start_version?: string;
  end_version?: string;
}

export interface TypedValueList {
  values: TypedValue[];
}

export interface TypedValue {
  string_value?: string;
  int_value?: number | string;
  double_value?: number;
  bool_value?: boolean;
  time_value?: string;
  json_value?: string;
  bytes_value?: string;
  list_value?: TypedValueList;
  null_value?: "NULL_VALUE_UNSPECIFIED" | "NULL_VALUE" | number;
}

export interface ColumnValue {
  column_name: string;
  value_type: FieldValueType;
  value: TypedValue;
}

export interface SortSpec {
  field_name: string;
  desc?: boolean;
}

export type FilterOp =
  | "FILTER_OP_UNSPECIFIED"
  | "FILTER_OP_EQ"
  | "FILTER_OP_NE"
  | "FILTER_OP_GT"
  | "FILTER_OP_GTE"
  | "FILTER_OP_LT"
  | "FILTER_OP_LTE"
  | "FILTER_OP_IN"
  | "FILTER_OP_NOT_IN"
  | "FILTER_OP_LIKE"
  | "FILTER_OP_BETWEEN"
  | "FILTER_OP_NOT_LIKE"
  | number;

export interface FilterCond {
  column: string;
  op: FilterOp;
  values: TypedValue[];
}

export interface FilterGroup {
  conds: FilterCond[];
  logical?: "FILTER_LOGICAL_AND" | "FILTER_LOGICAL_OR" | number;
}

export interface FilterSpec {
  groups: FilterGroup[];
  group_logical?: "FILTER_LOGICAL_AND" | "FILTER_LOGICAL_OR" | number;
}

export interface DataSource {
  space_id: string;
  data_source_id: string;
  name: string;
  kind: string;
  market?: string;
  timezone?: string;
  config_json?: string;
  status: string;
  created_at?: string;
  updated_at?: string;
  attributes?: Record<string, string>;
}

export interface Subject {
  space_id: string;
  subject_id: string;
  subject_type: string;
  name: string;
  market?: string;
  currency?: string;
  timezone?: string;
  status: string;
  created_at?: string;
  updated_at?: string;
  attributes?: Record<string, string>;
}

export interface SubjectSymbol {
  space_id: string;
  subject_id: string;
  data_source_id: string;
  external_symbol: string;
  status: string;
  created_at?: string;
  updated_at?: string;
  attributes?: Record<string, string>;
}

export interface Dataset {
  space_id: string;
  dataset_id: string;
  data_source_id: string;
  name: string;
  description?: string;
  data_kind: DataKind;
  freqs?: string[];
  status: string;
  created_at?: string;
  updated_at?: string;
  attributes?: Record<string, string>;
}

export interface DatasetSubject {
  space_id: string;
  dataset_id: string;
  subject_id: string;
  subject_role: string;
  effective_start_time?: string;
  effective_end_time?: string;
  status: string;
  created_at?: string;
  updated_at?: string;
  attributes?: Record<string, string>;
}

export interface Field {
  space_id: string;
  group_id: string;
  field_id: string;
  name: string;
  description?: string;
  value_type: FieldValueType;
  unit?: string;
  validation_rule_json?: string;
  write_example?: string;
  sort_order?: number;
  status: string;
  created_at?: string;
  updated_at?: string;
  attributes?: Record<string, string>;
}

export interface FieldGroup {
  space_id: string;
  group_id: string;
  name: string;
  description?: string;
  parent_group_id?: string;
  sort_order?: number;
  status: string;
  created_at?: string;
  updated_at?: string;
  attributes?: Record<string, string>;
}

export interface Factor {
  space_id: string;
  factor_id: string;
  name: string;
  description?: string;
  algorithm: string;
  params_json?: string;
  value_type: FieldValueType;
  status: string;
  created_at?: string;
  updated_at?: string;
  attributes?: Record<string, string>;
}

export interface DatasetColumn {
  space_id: string;
  dataset_id: string;
  column_name: string;
  origin_type: DatasetColumnOriginType;
  origin_id: string;
  value_type: FieldValueType;
  required?: boolean;
  is_unique?: boolean;
  aliases?: string[];
  status: string;
  created_at?: string;
  updated_at?: string;
  attributes?: Record<string, string>;
}

export interface View {
  space_id: string;
  view_id: string;
  name: string;
  description?: string;
  primary_dataset_id: string;
  dataset_ids?: string[];
  grain_keys?: string[];
  filter_json?: string;
  engine?: string;
  retention_window?: string;
  active_index_id?: string;
  status: string;
  columns?: ViewColumn[];
  created_at?: string;
  updated_at?: string;
  attributes?: Record<string, string>;
  view_version?: number | string;
  active_view_version?: number | string;
  active_columns?: ViewColumn[];
  index_build?: ViewIndexBuild;
}

export interface ViewIndexBuild {
  space_id: string;
  view_id: string;
  build_id: string;
  index_id: string;
  engine: string;
  target_view_version: number | string;
  state: number | string;
  owner_id?: string;
  lease_expires_at?: string;
  cursor_json?: string;
  snapshot_end?: string;
  coverage_start?: string;
  coverage_end?: string;
  entries_written?: number | string;
  schema_hash?: string;
  columns?: ViewColumn[];
  started_at?: string;
  updated_at?: string;
  finished_at?: string;
  error?: string;
}

export interface ViewColumn {
  space_id: string;
  view_id: string;
  column_name: string;
  origin_type: ColumnOriginType;
  origin_id: string;
  value_type: FieldValueType;
  online_time?: string;
  sort_order?: number;
  created_at?: string;
  updated_at?: string;
  attributes?: Record<string, string>;
}

export interface ResultColumn {
  column_name: string;
  value_type: FieldValueType;
  origin_type: ColumnOriginType;
  dataset_id?: string;
  origin_id?: string;
}

export interface PrimaryStoreNode {
  node_id: string;
  name: string;
  endpoint: string;
  weight?: number;
  status: string;
  config_json?: string;
  created_at?: string;
  updated_at?: string;
  attributes?: Record<string, string>;
}

export interface PrimaryStoreRoute {
  space_id: string;
  route_id: string;
  dataset_id: string;
  subject_id?: string;
  subject_pattern?: string;
  hash_rule?: string;
  node_id: string;
  priority?: number;
  status: string;
  created_at?: string;
  updated_at?: string;
  attributes?: Record<string, string>;
}

export interface ArchiveFile {
  space_id: string;
  archive_file_id: string;
  dataset_id: string;
  device_id: string;
  partition_key?: string;
  file_uri: string;
  file_format: string;
  min_time?: string;
  max_time?: string;
  row_count?: number | string;
  content_hash?: string;
  columns?: string[];
  status: string;
  created_at?: string;
  updated_at?: string;
  attributes?: Record<string, string>;
}

export interface TimeSeriesKey {
  space_id: string;
  dataset_id: string;
  subject_id: string;
  freq: string;
  dimensions?: Record<string, string>;
  data_time?: string;
}

export interface TimeSeriesRow {
  key: TimeSeriesKey;
  columns?: ColumnValue[];
  attributes?: Record<string, string>;
}

export interface RecordKey {
  space_id: string;
  dataset_id: string;
  record_id: string;
  version?: string;
}

export interface RecordRow {
  key: RecordKey;
  columns?: ColumnValue[];
  attributes?: Record<string, string>;
}
