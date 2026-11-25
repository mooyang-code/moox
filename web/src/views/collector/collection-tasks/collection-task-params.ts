import type { CollectorTask, CollectionTaskPayload, ExtraFieldOption, OutputFieldOption } from "@/api/collector";
import { normalizeFrequency } from "@/utils/frequency";
import { marketLabel } from "@/views/data/shared/series-market";
import { getTaskResultDatasetIds, getTaskResultState } from "@/views/collector/task-results/task-results-model";

export type CollectionTaskDataType = "kline" | "kline_resample";

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
 * （所以同时选了现货、永续的标签时选不了资金费率）。没选标签时一个都不可选。与后端建任务的校验一致。
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

/** 基础输出字段的 ID 集合：来自 GetDataTypeConfigWithFields 的 base_output_fields（结果数据集能建的列）。 */
export function baseOutputFieldIds(options: ReadonlyArray<OutputFieldOption> | undefined): Set<string> {
  return new Set((options || []).map(option => option.field_id));
}

/** 输出字段目录里的一项：显示名与分组只在 Collector 的字段规格里登记，由 GetDataTypeConfigWithFields 给出。 */
export interface OutputFieldEntry {
  field_id: string;
  display_name: string;
  group: string;
}

/** 没有分组的字段放在这一组。 */
export const UNGROUPED_OUTPUT_FIELDS = "其他";

/**
 * 输出字段目录：基础输出字段在前（按建列顺序），附加字段在后；附加字段按市场、品种、频率各登记一条，这里按字段 ID 去重。
 */
export function buildOutputFieldCatalog(
  base: ReadonlyArray<OutputFieldOption> | undefined,
  extra: ReadonlyArray<ExtraFieldOption> | undefined
): OutputFieldEntry[] {
  const entries = new Map<string, OutputFieldEntry>();
  for (const option of [...(base || []), ...(extra || [])]) {
    const id = option.field_id?.trim();
    if (!id || entries.has(id)) continue;
    entries.set(id, {
      field_id: id,
      display_name: option.display_name?.trim() || id,
      group: option.group?.trim() || UNGROUPED_OUTPUT_FIELDS
    });
  }
  return [...entries.values()];
}

/** 字段 ID 到显示名：目录里没有的显示字段 ID。 */
export function outputFieldLabels(ids: ReadonlyArray<string>, catalog: ReadonlyArray<OutputFieldEntry>) {
  const names = new Map(catalog.map(entry => [entry.field_id, entry.display_name]));
  return ids.map(id => ({ id, name: names.get(id) || id }));
}

export interface OutputFieldSection {
  name: string;
  fields: OutputFieldEntry[];
}

/** 按分组归并要显示的字段：分组按第一次出现的顺序，组内保持目录顺序；keyword 匹配显示名或字段 ID。 */
export function outputFieldSections(
  catalog: ReadonlyArray<OutputFieldEntry>,
  visible: (fieldId: string) => boolean,
  keyword = ""
): OutputFieldSection[] {
  const folded = keyword.trim().toLocaleLowerCase();
  const sections = new Map<string, OutputFieldEntry[]>();
  for (const entry of catalog) {
    if (!visible(entry.field_id)) continue;
    if (folded && !`${entry.display_name} ${entry.field_id}`.toLocaleLowerCase().includes(folded)) continue;
    const list = sections.get(entry.group) || [];
    list.push(entry);
    sections.set(entry.group, list);
  }
  return [...sections.entries()].map(([name, fields]) => ({ name, fields }));
}

/** 输出字段清单里是否显示这个字段：基础输出字段总是显示，其余只有当前可选的附加字段才显示。 */
export function outputFieldVisible(fieldId: string, available: ReadonlySet<string>, base: ReadonlySet<string>): boolean {
  return base.has(fieldId) || available.has(foldText(fieldId));
}

/**
 * 条件不满足时是否去掉已选的不可选字段。字段清单（基础与附加）还没读到时判断不了，保留；编辑已有任务时标签为空
 * （还没读到或读取失败）同样保留；其余情况去掉。提交前的过滤与条件变化时的清理用同一个判断。
 */
export function shouldDropUnavailableFields(editing: boolean, routeCount: number, optionsLoaded: boolean): boolean {
  return optionsLoaded && !(editing && routeCount === 0);
}

/**
 * 去掉已选字段里当前不可选的附加字段：确实有要去掉的才返回新数组，否则原样返回同一个数组
 * （调用方据此判断要不要赋值，避免监听已选字段时反复触发）。
 */
export function pruneUnavailableFields(
  fields: string[],
  available: ReadonlySet<string>,
  drop: boolean,
  base: ReadonlySet<string>
): string[] {
  if (!drop || fields.every(id => outputFieldVisible(id, available, base))) return fields;
  return fields.filter(id => outputFieldVisible(id, available, base));
}

/**
 * 这个字段现在能不能勾上。新建任务都可以；编辑已有任务时输出字段只能减少：只有任务原来就有的字段可以重新勾上，
 * 原来没有显式选择（用默认字段）时可以收窄到基础字段，但不能勾附加字段。与后端改任务的校验一致。
 */
export function outputFieldSelectable(
  fieldId: string,
  editing: boolean,
  originalFields: ReadonlyArray<string>,
  base: ReadonlySet<string>
): boolean {
  if (!editing) return true;
  if (!originalFields.length) return base.has(fieldId);
  return originalFields.some(id => foldText(id) === foldText(fieldId));
}

export type CollectionTaskMarket = "spot" | "swap" | "equity";

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
  /** 新建 K 线任务的采集方式（stream、scf、local）；空表示用市场默认值 */
  ingest?: string;
}

/** 采集方式的三个取值与界面文字。 */
export const INGEST_OPTIONS: ReadonlyArray<{ value: string; label: string; hint: string }> = [
  { value: "stream", label: "推送", hint: "collector-stream 接收交易所推送，补采走它的本地执行端" },
  { value: "scf", label: "云函数拉取", hint: "云函数定时拉取，补采也走云函数" },
  { value: "local", label: "本地拉取", hint: "collector-stream 本地定时拉取与补采" }
];

/** 采集方式的界面文字；合成任务没有采集方式，显示 “-”。 */
export function ingestLabel(value: unknown): string {
  const ingest = String(value ?? "").trim();
  return INGEST_OPTIONS.find(option => option.value === ingest)?.label || ingest || "-";
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
  ingest: string;
  tag_ids: string[];
  collect_params: Record<string, unknown>;
  enabled: boolean;
  creator: string;
  created_at: string;
  updated_at: string;
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
    ...(input.dataType === "kline" && input.ingest?.trim() ? { ingest: input.ingest.trim() } : {}),
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
    ingest: stringValue(value.ingest),
    tag_ids: stringArrayValue(value.tag_ids),
    collect_params: normalizeObject(value.collect_params),
    enabled: value.enabled !== false && value.enabled !== "false",
    creator: stringValue(value.creator),
    created_at: stringValue(value.created_at),
    updated_at: stringValue(value.updated_at),
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

function normalizeDataType(value: string): CollectionTaskDataType {
  if (value === "kline_resample") return "kline_resample";
  return "kline";
}

export function normalizeMarket(value: string): CollectionTaskMarket {
  return value === "swap" || value === "equity" ? value : "spot";
}

/** 一个标签的最小描述：创建任务时按它判断来源和市场。 */
export interface TaskTagRoute {
  tag_id: string;
  source: string;
  market: string;
}

export interface TaskGroupPlan {
  /** 拆分时加在任务名后面的市场名；只有一个市场时为空。 */
  suffix: string;
  tagIds: string[];
}

/** 选中的标签跨了市场：创建时按市场拆成多个任务（一个任务的标签只能属于一个市场）。 */
export function isCrossMarketSelection(tags: TaskTagRoute[]): boolean {
  return new Set(tags.map(tag => tag.market)).size > 1;
}

/** 选中的标签能否一起建任务：来源必须相同；跨市场时只允许现货和永续（拆成两个任务）。返回不能的原因，没有问题时返回空。 */
export function taskTagSelectionError(tags: TaskTagRoute[]): string {
  if (new Set(tags.map(tag => tag.source)).size > 1) {
    return "同一任务的标签必须来自同一来源；来源不同的标签请分别建任务";
  }
  const markets = [...new Set(tags.map(tag => tag.market))];
  if (markets.length > 1 && !(markets.length === 2 && markets.includes("spot") && markets.includes("swap"))) {
    return "只有现货和永续两个市场的标签可以一起选（按市场拆成两个任务）";
  }
  return "";
}

/** 把选中的标签分成要创建的任务：只有一个市场时就是一个任务；跨市场时按市场各一个任务，现货在前。 */
export function planTaskGroups(tags: TaskTagRoute[]): TaskGroupPlan[] {
  if (!isCrossMarketSelection(tags)) {
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
    dataset_id: stringValue(result.dataset_id),
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
  if (state === "stale" && prepared && getTaskResultDatasetIds(task as CollectorTask).length) return "结果可用";
  return "结果准备中";
}

/** 打开编辑抽屉的请求：每次打开给一个新对象，抽屉按它重置表单。 */
export type TaskEditorRequest =
  | { mode: "create" }
  | { mode: "edit"; record: CollectionTaskRecord }
  | { mode: "resample"; source: CollectionTaskRecord };
