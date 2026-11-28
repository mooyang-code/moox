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

export type { Page, PageResult, TotalMode, TotalState } from "@/api/page";

export interface RetInfo {
  code: number | string;
  msg: string;
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
  null_value?: "NULL_VALUE_UNSPECIFIED" | "NULL_VALUE_NULL" | number;
}

export interface FieldValue {
  field_id: string;
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
  status: string;
  /** 创建、更新时刻：Unix 毫秒（数字字符串）。 */
  created_at?: string;
  updated_at?: string;
  /** 业务含义放在属性里，Storage 原样保存。 */
  attributes?: Record<string, string>;
}

export interface Subject {
  space_id: string;
  subject_id: string;
  name: string;
  timezone?: string;
  status: string;
  /** 创建、更新时刻：Unix 毫秒（数字字符串）。 */
  created_at?: string;
  updated_at?: string;
  attributes?: Record<string, string>;
}

export type TagMode = "auto" | "manual";
export type TagMemberStatus = "active" | "inactive";

export interface Tag {
  space_id: string;
  tag_id: string;
  tag_name: string;
  description?: string;
  mode: TagMode;
  builtin?: boolean;
  active_count?: number;
  inactive_count?: number;
  /** 创建、更新时刻：Unix 毫秒（数字字符串）。 */
  created_at?: string;
  updated_at?: string;
  /** 市场类型与同步配置放在属性里：market_type、sync.source、sync.cron、sync.timezone。 */
  attributes?: Record<string, string>;
}

export interface TagMember {
  space_id: string;
  tag_id?: string;
  status?: TagMemberStatus;
  /** 失效时刻：Unix 毫秒（数字字符串），在册成员为 "0"。 */
  inactive_at?: string;
  subject: Subject;
  tag_ids?: string[];
}

export interface TagReference {
  dataset_id: string;
  dataset_name?: string;
  collector_task_id?: string;
}

export interface Dataset {
  space_id: string;
  dataset_id: string;
  data_source_id: string;
  name: string;
  description?: string;
  data_kind: DataKind;
  /** 时序 Dataset 的唯一频率；记录型 Dataset 可为空。 */
  frequency?: string;
  status: string;
  /** 只读的生效保留期："<n>h" 或 "forever"，由 Storage 策略按空间与频率解析。 */
  retention?: string;
  /** 保留期来源：default（全局默认）、space（空间覆盖）、record（记录型不过期）。 */
  retention_source?: string;
  revision?: number | string;
  subject_tags?: string[];
  /** 周期对齐方式：epoch_utc 或 session。 */
  alignment?: string;
  /** 周期截止前允许迟到的秒数；0 或缺省表示用 Storage 的默认值。 */
  allowed_lateness_seconds?: number;
  /** 查询索引维护的先后：越小越先，0（缺省）最先。 */
  maintenance_priority?: number;
  /** 创建、更新时刻：Unix 毫秒（数字字符串）。 */
  created_at?: string;
  updated_at?: string;
  attributes?: Record<string, string>;
}

export interface DatasetColumn {
  space_id: string;
  dataset_id: string;
  column_name: string;
  value_type: FieldValueType;
  /** 界面显示名，建列时必填。 */
  display_name: string;
  /** 界面分组名（中文，一级），可以为空。 */
  group?: string;
  required?: boolean;
  aliases?: string[];
  /** active 或 disabled：disabled 的列读取时隐藏，数据保留。 */
  status: string;
  /** 创建、更新时刻：Unix 毫秒（数字字符串）。 */
  created_at?: string;
  updated_at?: string;
  attributes?: Record<string, string>;
}

export interface ResultColumn {
  column_name: string;
  value_type: FieldValueType;
  display_name?: string;
}

export interface TimeSeriesSelector {
  subject_id: string;
  frequency: string;
  series_tag?: string;
}

export interface TimeSeriesRowKey {
  subject_id: string;
  frequency: string;
  data_time: string;
  series_tag?: string;
}

export interface RecordRowKey {
  record_id: string;
  /** 检索请求（SearchRecordRows）里省略或空值表示匹配任意版本（由 version_range 约束）；写入与响应中非空。 */
  version?: string;
}

export interface RowKey {
  space_id: string;
  dataset_id: string;
  time_series?: TimeSeriesRowKey;
  record?: RecordRowKey;
}

/** Storage 读写共用的行消息；DataView 查询响应与 ReadTimeSeriesRows 不返回 attributes。 */
export interface Row {
  key: RowKey;
  fields?: FieldValue[];
  attributes?: Record<string, TypedValue>;
}
