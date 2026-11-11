import type { CollectorTask, CollectionTaskPayload, ExtraFieldOption } from "@/api/collector";
import { normalizeFrequency } from "@/utils/frequency";
import { marketLabel } from "@/views/data/shared/series-market";
import { getTaskResultState, getTaskResultViewIds } from "@/views/collector/task-results/task-results-model";

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

/** 附加字段的可选条件：任务类型、所在空间（市场）、频率、所选标签的品种，以及接口给出的附加字段清单。 */
export interface ExtraFieldCondition {
  dataType: string;
  spaceId: string;
  frequency: string;
  routes: ReadonlyArray<{ market: string }>;
  options: ReadonlyArray<ExtraFieldOption>;
}

const foldText = (value: string | undefined) => (value || "").trim().toLowerCase();

/**
 * 当前可选的附加字段：只有 K 线采集有；字段要在本空间、当前频率下可用，并且所选的每个标签的品种都有来源
 * （所以现货加永续的统一数据集选不了资金费率）。没选标签时一个都不可选。与后端建任务的校验一致。
 */
export function availableExtraFieldIds(condition: ExtraFieldCondition): Set<string> {
  const available = new Set<string>();
  if (condition.dataType !== "kline" || !condition.routes.length) return available;
  const space = foldText(condition.spaceId);
  const frequency = normalizeFrequency(condition.frequency || "");
  const instruments = [...new Set(condition.routes.map(route => foldText(route.market)))];
  const offered = new Map<string, Set<string>>();
  for (const option of condition.options) {
    if (foldText(option.market_id) !== space) continue;
    if (!(option.frequencies || []).some(item => normalizeFrequency(item) === frequency)) continue;
    const id = foldText(option.field_id);
    offered.set(id, (offered.get(id) || new Set<string>()).add(foldText(option.instrument_type)));
  }
  for (const [id, markets] of offered) {
    if (instruments.every(instrument => markets.has(instrument))) available.add(id);
  }
  return available;
}

/** 输出字段清单里是否显示这个字段：基础 K 线字段总是显示，其余只有当前可选的附加字段才显示。 */
export function outputFieldVisible(fieldId: string, available: ReadonlySet<string>): boolean {
  return KLINE_OUTPUT_FIELD_IDS.has(fieldId) || available.has(foldText(fieldId));
}

/**
 * 条件不满足时是否去掉已选的附加字段。编辑已有任务时，标签为空（还没读到或读取失败）或附加字段清单还没读到，
 * 都判断不了条件，保留；其余情况去掉。提交前的过滤与条件变化时的清理用同一个判断。
 */
export function shouldDropUnavailableFields(editing: boolean, routeCount: number, optionsLoaded: boolean): boolean {
  return !(editing && (routeCount === 0 || !optionsLoaded));
}

/**
 * 响应里有没有带附加字段清单。旧版 Collector 的响应没有 extra_fields，这时不能当成“清单为空”：
 * 否则编辑已有任务时会把已选的附加字段（例如 funding_rate）当成不可选去掉再提交。
 */
export function extraFieldOptionsPresent(detail: { extra_fields?: unknown } | null | undefined): boolean {
  return Array.isArray(detail?.extra_fields);
}

/**
 * 去掉已选字段里当前不可选的附加字段：确实有要去掉的才返回新数组，否则原样返回同一个数组
 * （调用方据此判断要不要赋值，避免监听已选字段时反复触发）。
 */
export function pruneUnavailableFields(fields: string[], available: ReadonlySet<string>, drop: boolean): string[] {
  if (!drop || fields.every(id => outputFieldVisible(id, available))) return fields;
  return fields.filter(id => outputFieldVisible(id, available));
}

/**
 * 这个字段现在能不能勾上。新建任务都可以；编辑已有任务时输出字段只能减少：只有任务原来就有的字段可以重新勾上，
 * 原来没有显式选择（用默认字段）时可以收窄到基础字段，但不能勾附加字段。与后端改任务的校验一致。
 */
export function outputFieldSelectable(fieldId: string, editing: boolean, originalFields: ReadonlyArray<string>): boolean {
  if (!editing) return true;
  if (!originalFields.length) return KLINE_OUTPUT_FIELD_IDS.has(fieldId);
  return originalFields.some(id => foldText(id) === foldText(fieldId));
}

export type CollectionTaskMarket = "spot" | "swap" | "equity" | "multi";

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
  /** 源是统一数据集（现货加永续）时各条源序列的 series_tag，此时 sourceSeriesTag 为空 */
  sourceSeriesTags?: string[];
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
  /** 源是统一数据集时各条源序列的 series_tag，与 source_series_tag 只会给出一个 */
  source_series_tags?: string[];
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
    ...(stringArrayValue(params.source_series_tags).length
      ? { source_series_tags: stringArrayValue(params.source_series_tags) }
      : {}),
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
      sourceSeriesTags: resample.source_series_tags || [],
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

function normalizeDataType(value: string): CollectionTaskDataType {
  if (value === "kline_resample") return "kline_resample";
  return "kline";
}

export function normalizeMarket(value: string): CollectionTaskMarket {
  return value === "swap" || value === "equity" || value === "multi" ? value : "spot";
}

/** 一个标签的最小描述：创建任务时按它判断来源和市场。 */
export interface TaskTagRoute {
  tag_id: string;
  source: string;
  market: string;
}

/** 创建任务时两个市场的组织方式：合并成一个统一数据集，或按市场拆成两个任务。 */
export type TaskOrganization = "unified" | "split";

export interface TaskGroupPlan {
  /** 拆分时加在任务名后面的市场名；合并（单组）时为空。 */
  suffix: string;
  tagIds: string[];
}

/**
 * 选中的标签跨了现货、永续两个市场（同一来源）。后端只认这一种跨市场：来源不同的标签不能放进同一个任务。
 */
export function isCrossMarketSelection(tags: TaskTagRoute[]): boolean {
  return new Set(tags.map(tag => tag.market)).size > 1;
}

/** 选中的标签能否放进一个任务：来源必须相同；跨市场时只允许现货和永续。返回不能的原因，没有问题时返回空。 */
export function taskTagSelectionError(tags: TaskTagRoute[]): string {
  if (new Set(tags.map(tag => tag.source)).size > 1) {
    return "同一任务的标签必须来自同一来源；来源不同的标签请分别建任务";
  }
  const markets = [...new Set(tags.map(tag => tag.market))];
  if (markets.length > 1 && !(markets.length === 2 && markets.includes("spot") && markets.includes("swap"))) {
    return "只有现货和永续两个市场可以放进同一个任务";
  }
  return "";
}

/**
 * 按组织方式把选中的标签分成要创建的任务：合并（或没有跨市场）就是一个任务；拆分按市场各一个任务，现货在前。
 */
export function planTaskGroups(tags: TaskTagRoute[], organization: TaskOrganization): TaskGroupPlan[] {
  if (!isCrossMarketSelection(tags) || organization === "unified") {
    return [{ suffix: "", tagIds: tags.map(tag => tag.tag_id) }];
  }
  const byMarket = new Map<string, string[]>();
  for (const tag of tags) byMarket.set(tag.market, [...(byMarket.get(tag.market) || []), tag.tag_id]);
  return [...byMarket.entries()]
    .sort(([left], [right]) => (left === "spot" ? -1 : right === "spot" ? 1 : left.localeCompare(right)))
    .map(([market, tagIds]) => ({ suffix: marketLabel(market), tagIds }));
}

/** 拆分后各任务的名字：原名加市场名。 */
export function splitTaskName(name: string, suffix: string): string {
  return suffix ? `${normalizeCollectionTaskName(name)}（${suffix}）` : normalizeCollectionTaskName(name);
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
 * 采集任务列表上的结果文字：和采集结果页共用一套判定（getTaskResultState）。
 * 列表接口不逐个检查 Storage，结果状态是 unknown（判定为待刷新）：这时按 Collector 自己的准备状态，ready 表示结果已经建好。
 */
export function collectionTaskResultLabel(
  task: Pick<CollectionTaskRecord, "prepare_state" | "last_error" | "result">
): CollectionTaskResultLabel {
  const state = getTaskResultState(task as CollectorTask);
  if (state === "error") return "结果异常";
  if (state === "ready") return "结果可用";
  const prepared = String(task.prepare_state || "").toLowerCase() === "ready";
  if (state === "stale" && prepared && getTaskResultViewIds(task as CollectorTask).length) return "结果可用";
  return "结果准备中";
}
