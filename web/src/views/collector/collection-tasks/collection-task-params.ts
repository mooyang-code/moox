import type { CollectorTask, CollectionTaskPayload } from "@/api/collector";

export type CollectionTaskDataType = "kline" | "kline_resample";
export type CollectionTaskMarket = "spot" | "swap";

export interface CollectionSourceOption {
  source_id: string;
  name?: string;
  data_source_id: string;
  data_kind: string | number;
  attributes?: Record<string, string>;
  freqs?: string[];
  keep_duration?: string;
}

export interface CollectionTaskInput {
  dataType: CollectionTaskDataType;
  provider: string;
  market: CollectionTaskMarket;
  frequency: string;
  subjectTags?: string[];
  sourceId?: string;
  sourceFrequency?: string;
  sourceSeriesTag?: string;
  settleDelayMS?: number;
  outputFields?: string[];
  tagIds?: string[];
}

export interface KlineResampleParams extends Record<string, unknown> {
  provider: string;
  market_type: CollectionTaskMarket;
  source_dataset_id: string;
  source_frequency: string;
  source_series_tag: string;
  target_frequency: string;
  alignment: "epoch_utc";
  settle_delay_ms?: number;
  output_fields?: string[];
  subject_tags?: string[];
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
  const sourceId = input.sourceId?.trim();
  const sourceFrequency = input.sourceFrequency?.trim();
  const sourceSeriesTag = input.sourceSeriesTag?.trim();
  const provider = input.provider.trim().toLowerCase();
  const market = input.market;
  const frequency = input.frequency.trim();
  if (!frequency) throw new Error("请输入采集频率");
  if (!sourceId || !sourceFrequency || !sourceSeriesTag) {
    throw new Error("请填写源行情、源周期和序列标签");
  }
  const params: KlineResampleParams = {
    provider,
    market_type: market,
    source_dataset_id: sourceId,
    source_frequency: sourceFrequency,
    source_series_tag: sourceSeriesTag,
    target_frequency: frequency,
    alignment: "epoch_utc"
  };
  const subjectTags = normalizeSubjectTags(input.subjectTags);
  if (subjectTags.length > 0) params.subject_tags = subjectTags;
  if (Number.isFinite(input.settleDelayMS) && (input.settleDelayMS as number) >= 0) {
    params.settle_delay_ms = Math.trunc(input.settleDelayMS as number);
  }
  const outputFields = [...new Set((input.outputFields || []).map(value => value.trim()).filter(Boolean))];
  if (outputFields.length) params.output_fields = outputFields;
  return params;
}

export function parseKlineResampleParams(params: Record<string, unknown>): KlineResampleParams {
  return {
    provider: stringValue(params.provider),
    market_type: normalizeMarket(stringValue(params.market_type)),
    source_dataset_id: stringValue(params.source_dataset_id),
    source_frequency: stringValue(params.source_frequency),
    source_series_tag: stringValue(params.source_series_tag),
    target_frequency: stringValue(params.target_frequency),
    alignment: "epoch_utc",
    ...(numberValue(params.settle_delay_ms) !== undefined ? { settle_delay_ms: numberValue(params.settle_delay_ms) } : {}),
    ...(stringArrayValue(params.output_fields).length ? { output_fields: stringArrayValue(params.output_fields) } : {}),
    ...(stringArrayValue(params.subject_tags).length ? { subject_tags: stringArrayValue(params.subject_tags) } : {})
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
      provider: resample.provider,
      market: resample.market_type,
      frequency: resample.target_frequency,
      sourceId: resample.source_dataset_id,
      sourceFrequency: resample.source_frequency,
      sourceSeriesTag: resample.source_series_tag,
      settleDelayMS: resample.settle_delay_ms,
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
  if (frequency) {
    const requested = normalizeStorageFrequency(frequency);
    if (!requested) return false;
    const supported = (source.freqs || []).map(normalizeStorageFrequency).filter((value): value is string => Boolean(value));
    if (!supported.includes(requested)) return false;
  }
  return true;
}

function normalizeDataType(value: string): CollectionTaskDataType {
  if (value === "kline_resample") return "kline_resample";
  return "kline";
}

function normalizeMarket(value: string): CollectionTaskMarket {
  return value === "swap" ? "swap" : "spot";
}

function normalizeStorageFrequency(value: string): string | undefined {
  const match = value.trim().match(/^(\d+)([smhdwHDWMyY])$/);
  if (!match || Number(match[1]) <= 0) return undefined;
  const count = match[1];
  switch (match[2]) {
    case "s":
      return `${count}s`;
    case "m":
      return `${count}m`;
    case "h":
    case "H":
      return `${count}H`;
    case "d":
    case "D":
      return `${count}D`;
    case "w":
    case "W":
      return `${count}W`;
    case "M":
      return `${count}M`;
    case "y":
    case "Y":
      return `${count}Y`;
    default:
      return undefined;
  }
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

function numberValue(value: unknown): number | undefined {
  const parsed = Number(value);
  return Number.isFinite(parsed) && parsed >= 0 ? Math.trunc(parsed) : undefined;
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

/** Rows whose result status the list left uninspected. */
export function tasksNeedingResultInspection(tasks: CollectionTaskRecord[]): CollectionTaskRecord[] {
  return tasks.filter(task => task.result?.view_id && String(task.result.status || "").toLowerCase() === "unknown");
}

/**
 * Inspects the live result status of a page of tasks with bounded
 * concurrency. Each detail is applied as soon as it arrives; a failed lookup
 * leaves that row unchanged.
 */
export async function inspectCollectionTaskResults(
  tasks: CollectionTaskRecord[],
  fetchDetail: (task: CollectionTaskRecord) => Promise<CollectionTaskRecord | undefined>,
  apply: (detail: CollectionTaskRecord) => void,
  concurrency = 4
): Promise<void> {
  const queue = tasksNeedingResultInspection(tasks);
  const worker = async () => {
    for (let task = queue.shift(); task; task = queue.shift()) {
      try {
        const detail = await fetchDetail(task);
        if (detail) apply(detail);
      } catch {
        // Keep the preparation-state label for this row.
      }
    }
  };
  await Promise.all(Array.from({ length: Math.min(Math.max(concurrency, 1), queue.length) }, worker));
}
