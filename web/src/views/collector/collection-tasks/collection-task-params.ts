import type { CollectorTask, CollectionTaskPayload } from "@/api/collector";
import { normalizeFrequency } from "@/utils/frequency";

export type CollectionTaskDataType = "kline" | "kline_resample";
// K 线采集结果后端（taskresult.ensureColumns 的时序数据集）支持的全部输出字段；字段目录里的其他字段选了会被拒绝。
export const KLINE_OUTPUT_FIELD_IDS: ReadonlySet<string> = new Set([
  "open",
  "high",
  "low",
  "close",
  "volume",
  "quote_volume",
  "trade_num",
  "amount",
  "instrument_name",
  "provider_id",
  "source_id",
  "provider_symbol",
  "volume_unit",
  "amount_unit",
  "trade_date",
  "close_time",
  "provider_timestamp",
  "fetched_at",
  "request_id",
  "route_id",
  "route_rank",
  "source_provider",
  "quality_status",
  "amount_quality"
]);

export type CollectionTaskMarket = "spot" | "swap" | "equity";

export interface CollectionSourceOption {
  source_id: string;
  name?: string;
  data_source_id: string;
  data_kind: string | number;
  attributes?: Record<string, string>;
  freq?: string;
  retention?: string;
}

export interface CollectionTaskInput {
  dataType: CollectionTaskDataType;
  provider: string;
  market: CollectionTaskMarket;
  frequency: string;
  subjectTags?: string[];
  // 合成的源采集任务：创建时只提交它与目标频率，源数据集、源周期、序列标签由后端推导。
  sourceTaskId?: string;
  // 以下三项只用于展示已有合成任务（由后端推导后保存），不参与创建。
  sourceId?: string;
  sourceFrequency?: string;
  sourceSeriesTag?: string;
  outputFields?: string[];
  tagIds?: string[];
}

export interface KlineResampleParams extends Record<string, unknown> {
  source_task_id?: string;
  provider?: string;
  market_type?: CollectionTaskMarket;
  source_dataset_id?: string;
  source_frequency?: string;
  source_series_tag?: string;
  target_frequency: string;
  output_fields?: string[];
}

export interface CollectionTaskFields {
  task_name: string;
  description: string;
  data_type: CollectionTaskDataType;
  collect_params: Record<string, unknown>;
  enabled: boolean;
  creator: string;
  space_id: string;
  task_id?: string;
}

export interface CollectionTaskRecord {
  id?: number;
  task_id: string;
  task_name: string;
  description: string;
  space_id: string;
  data_type: string;
  tag_ids: string[];
  collect_params: Record<string, unknown>;
  enabled: boolean;
  creator: string;
  create_time: string;
  modify_time: string;
  prepare_state?: string;
  last_error?: string;
  result?: CollectorTask["result"];
}

export function normalizeCollectionTaskName(value: unknown): string {
  return String(value ?? "").trim();
}

export function collectionTaskNameError(value: unknown): string | undefined {
  const name = normalizeCollectionTaskName(value);
  if (!name) return "请输入任务名称";
  if (Array.from(name).length > 80) return "任务名称不能超过 80 个字符";
  return undefined;
}

export function buildCollectionTaskParams(input: CollectionTaskInput): Record<string, unknown> {
  const provider = input.provider.trim().toLowerCase();
  const market = input.market;
  const frequency = input.frequency.trim();
  const outputFields = [...new Set((input.outputFields || []).map(value => value.trim()).filter(Boolean))];
  if (!frequency) throw new Error("请输入采集频率");

  if (input.dataType === "kline_resample") {
    return buildKlineResampleParams({ ...input, provider, market, frequency, outputFields });
  }

  const subjectTags = normalizeSubjectTags(input.subjectTags);
  if (subjectTags.length === 0) throw new Error("请选择标的标签");
  return {
    subject_tags: subjectTags,
    frequency,
    ...(outputFields.length ? { output_fields: outputFields } : {})
  };
}

export function buildKlineResampleParams(input: CollectionTaskInput & { outputFields?: string[] }): KlineResampleParams {
  const sourceTaskId = input.sourceTaskId?.trim();
  const frequency = input.frequency.trim();
  if (!frequency) throw new Error("请输入目标频率");
  if (!sourceTaskId) throw new Error("请选择源采集任务");
  const params: KlineResampleParams = { source_task_id: sourceTaskId, target_frequency: normalizeFrequency(frequency) };
  const outputFields = [...new Set((input.outputFields || []).map(value => value.trim()).filter(Boolean))];
  if (outputFields.length) params.output_fields = outputFields;
  return params;
}

export function parseKlineResampleParams(params: Record<string, unknown>): KlineResampleParams {
  const provider = stringValue(params.provider);
  return {
    ...(stringValue(params.source_task_id) ? { source_task_id: stringValue(params.source_task_id) } : {}),
    ...(provider ? { provider } : {}),
    ...(provider || stringValue(params.market_type) ? { market_type: normalizeMarket(stringValue(params.market_type)) } : {}),
    source_dataset_id: stringValue(params.source_dataset_id),
    source_frequency: stringValue(params.source_frequency),
    source_series_tag: stringValue(params.source_series_tag),
    target_frequency: stringValue(params.target_frequency),
    ...(stringArrayValue(params.output_fields).length ? { output_fields: stringArrayValue(params.output_fields) } : {})
  };
}

export function buildCollectionTaskPayload(
  input: CollectionTaskInput,
  fields: Pick<CollectionTaskFields, "task_name" | "description" | "space_id" | "creator" | "enabled">,
  taskId?: string
): CollectionTaskPayload {
  return {
    ...(taskId ? { task_id: taskId } : {}),
    space_id: fields.space_id,
    task_name: normalizeCollectionTaskName(fields.task_name),
    description: fields.description.trim(),
    data_type: input.dataType,
    tag_ids: normalizeSubjectTags(input.tagIds || input.subjectTags),
    collect_params: buildCollectionTaskParams(input),
    enabled: fields.enabled,
    creator: fields.creator.trim()
  };
}

export function normalizeCollectionTask(raw: unknown): CollectionTaskRecord {
  const value = asRecord(raw);
  return {
    id: typeof value.id === "number" ? value.id : undefined,
    task_id: stringValue(value.task_id),
    task_name: stringValue(value.task_name) || stringValue(value.name) || stringValue(value.task_id),
    description: stringValue(value.description),
    space_id: stringValue(value.space_id),
    data_type: stringValue(value.data_type),
    tag_ids: stringArrayValue(value.tag_ids),
    collect_params: normalizeObject(value.collect_params),
    enabled: value.enabled !== false && value.enabled !== "false",
    creator: stringValue(value.creator),
    create_time: stringValue(value.create_time),
    modify_time: stringValue(value.modify_time),
    prepare_state: stringValue(value.prepare_state),
    last_error: stringValue(value.last_error),
    result: normalizeResult(value.result)
  };
}

export function parseCollectionTaskInput(
  task: Pick<CollectionTaskRecord, "data_type" | "tag_ids" | "collect_params">
): CollectionTaskInput {
  const params = task.collect_params;
  const dataType = normalizeDataType(task.data_type);
  if (dataType === "kline_resample") {
    const resample = parseKlineResampleParams(params);
    return {
      dataType,
      provider: resample.provider || "",
      market: resample.market_type || "spot",
      frequency: resample.target_frequency,
      sourceTaskId: resample.source_task_id,
      sourceId: resample.source_dataset_id,
      sourceFrequency: resample.source_frequency,
      sourceSeriesTag: resample.source_series_tag,
      outputFields: resample.output_fields || []
    };
  }
  return {
    dataType,
    provider: "",
    market: "spot",
    frequency: stringValue(params.frequency),
    subjectTags: task.tag_ids.length ? task.tag_ids.slice() : stringArrayValue(params.subject_tags),
    outputFields: stringArrayValue(params.output_fields)
  };
}

export function taskFrequency(task: Pick<CollectionTaskRecord, "data_type" | "collect_params">): string {
  const params = task.collect_params;
  return task.data_type === "kline_resample" ? stringValue(params.target_frequency) || "-" : stringValue(params.frequency) || "-";
}

export function collectionSourceMatches(
  source: CollectionSourceOption,
  provider: string,
  dataType: string,
  market?: string,
  frequency?: string
): boolean {
  const normalizedType = normalizeDataType(dataType);
  if (normalizedType !== "kline" && normalizedType !== "kline_resample" && source.data_source_id !== provider) {
    return false;
  }
  const expectedKinds =
    normalizedType === "kline" || normalizedType === "kline_resample"
      ? ["DATA_KIND_TIME_SERIES", "time_series", 2]
      : ["DATA_KIND_RECORD", "record", 1];
  if (!expectedKinds.includes(source.data_kind)) return false;
  if (market && source.attributes?.market_type?.toLowerCase() !== market.toLowerCase()) return false;
  if (normalizedType === "kline_resample" && source.attributes?.dataset_role === "kline_resample_result") return false;
  if (frequency && normalizeFrequency(source.freq || "") !== normalizeFrequency(frequency)) return false;
  return true;
}

function normalizeDataType(value: string): CollectionTaskDataType {
  if (value === "kline_resample") return "kline_resample";
  return "kline";
}

export function normalizeMarket(value: string): CollectionTaskMarket {
  return value === "swap" || value === "equity" ? value : "spot";
}

function normalizeResult(value: unknown): CollectorTask["result"] | undefined {
  if (!value || typeof value !== "object" || Array.isArray(value)) return undefined;
  const result = value as Record<string, unknown>;
  return {
    result_name: stringValue(result.result_name),
    view_id: stringValue(result.view_id),
    view_ids: Array.isArray(result.view_ids) ? result.view_ids.map(String).filter(Boolean) : undefined,
    status: stringValue(result.status),
    last_data_time: stringValue(result.last_data_time),
    coverage_start: stringValue(result.coverage_start),
    coverage_end: stringValue(result.coverage_end),
    data_kind: stringValue(result.data_kind)
  };
}

function normalizeObject(value: unknown): Record<string, unknown> {
  if (value && typeof value === "object" && !Array.isArray(value)) return value as Record<string, unknown>;
  if (typeof value === "string") {
    try {
      const parsed: unknown = JSON.parse(value);
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) return parsed as Record<string, unknown>;
    } catch {
      return {};
    }
  }
  return {};
}

function asRecord(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value) ? (value as Record<string, unknown>) : {};
}

function stringValue(value: unknown): string {
  return typeof value === "string" ? value.trim() : value === undefined || value === null ? "" : String(value).trim();
}

function normalizeSubjectTags(tags?: string[]): string[] {
  return [...new Set((tags || []).map(item => item.trim()).filter(Boolean))];
}

function stringArrayValue(value: unknown): string[] {
  return Array.isArray(value) ? value.map(item => stringValue(item)).filter(Boolean) : [];
}

export type CollectionTaskResultLabel = "结果可用" | "结果准备中" | "结果异常";

/**
 * Result label for a task. The task list does not inspect Storage per task, so
 * it reports result status "unknown"; the label then follows the Collector's
 * own preparation state, where "ready" means the result is provisioned and
 * active. Task details carry the inspected Storage status.
 */
export function collectionTaskResultLabel(
  task: Pick<CollectionTaskRecord, "prepare_state" | "last_error" | "result">
): CollectionTaskResultLabel {
  const status = String(task.result?.status || "").toLowerCase();
  const prepareState = String(task.prepare_state || "").toLowerCase();
  if (status === "error" || prepareState === "error" || task.last_error) return "结果异常";
  if (!task.result?.view_id) return "结果准备中";
  if (["active", "ready", "succeeded"].includes(status)) return "结果可用";
  if (status === "unknown" && prepareState === "ready") return "结果可用";
  return "结果准备中";
}
