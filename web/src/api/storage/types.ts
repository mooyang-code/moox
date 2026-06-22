export type DataKind =
  | 'DATA_KIND_UNSPECIFIED'
  | 'DATA_KIND_RECORD'
  | 'DATA_KIND_TIME_SERIES'
  | 'DATA_KIND_SNAPSHOT'
  | 'DATA_KIND_EVENT'
  | 'DATA_KIND_DOCUMENT'
  | 'DATA_KIND_TABLE'
  | number;

export type FieldValueType =
  | 'FIELD_VALUE_TYPE_UNSPECIFIED'
  | 'FIELD_VALUE_TYPE_STRING'
  | 'FIELD_VALUE_TYPE_INT'
  | 'FIELD_VALUE_TYPE_DOUBLE'
  | 'FIELD_VALUE_TYPE_BOOL'
  | 'FIELD_VALUE_TYPE_TIME'
  | 'FIELD_VALUE_TYPE_JSON'
  | 'FIELD_VALUE_TYPE_BYTES'
  | number;

export type DatasetColumnOriginType =
  | 'DATASET_COLUMN_ORIGIN_TYPE_UNSPECIFIED'
  | 'DATASET_COLUMN_ORIGIN_TYPE_FIELD'
  | 'DATASET_COLUMN_ORIGIN_TYPE_FACTOR'
  | 'DATASET_COLUMN_ORIGIN_TYPE_SYSTEM'
  | number;

export type ColumnOriginType =
  | 'COLUMN_ORIGIN_TYPE_UNSPECIFIED'
  | 'COLUMN_ORIGIN_TYPE_DATASET_COLUMN'
  | 'COLUMN_ORIGIN_TYPE_SYSTEM'
  | 'COLUMN_ORIGIN_TYPE_EXPRESSION'
  | number;

export type SortOrder = 'SORT_ORDER_ASC' | 'SORT_ORDER_DESC' | number;

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

export interface FilterExpr {
  expr: string;
  args?: Record<string, TypedValue>;
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
  field_id: string;
  name: string;
  description?: string;
  value_type: FieldValueType;
  unit?: string;
  validation_rule_json?: string;
  write_example?: string;
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
  query_window?: string;
  active_result?: string;
  build_status?: string;
  status: string;
  columns?: ViewColumn[];
  created_at?: string;
  updated_at?: string;
  attributes?: Record<string, string>;
  view_version?: number | string;
  active_view_version?: number | string;
  building_view_version?: number | string;
  building_result?: string;
  build_error?: string;
  build_started_at?: string;
  build_finished_at?: string;
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
