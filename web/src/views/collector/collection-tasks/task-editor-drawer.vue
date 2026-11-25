<template>
  <a-drawer
    v-model:visible="visible"
    :title="editorTitle"
    width="680px"
    :mask-closable="false"
    unmount-on-close
    @close="afterClose"
  >
    <a-form ref="formRef" auto-label-width :rules="rules" :model="editor" layout="vertical">
      <a-alert v-if="editing" type="info" show-icon class="immutable-hint">
        数据类型、标签范围、结果身份和结果形态不可修改，如需变更请创建新任务。
      </a-alert>
      <section class="editor-section">
        <header class="editor-section__header">
          <h3>任务信息</h3>
          <span>用于识别和说明这项采集任务</span>
        </header>
        <a-form-item field="task_name" label="任务名称" validate-trigger="blur">
          <a-input
            v-model="editor.task_name"
            placeholder="例如：币安现货 1 小时行情"
            allow-clear
            :max-length="80"
            show-word-limit
          />
        </a-form-item>
        <a-form-item field="description" label="描述">
          <a-textarea
            v-model="editor.description"
            placeholder="补充任务用途、数据范围或负责人信息"
            :max-length="200"
            show-word-limit
            allow-clear
          />
        </a-form-item>
        <a-row v-if="editing" :gutter="16">
          <a-col :span="12">
            <a-form-item field="enabled" label="启用状态" required>
              <a-select v-model="editor.enabled"
                ><a-option :value="true">启用</a-option><a-option :value="false">禁用</a-option></a-select
              >
            </a-form-item>
          </a-col>
          <a-col :span="12">
            <a-form-item label="任务 ID"><a-input :model-value="editor.task_id" disabled /></a-form-item>
          </a-col>
        </a-row>
        <a-form-item v-if="editing" label="创建人"><a-input :model-value="editor.creator || '-'" disabled /></a-form-item>
      </section>

      <section class="editor-section">
        <header class="editor-section__header">
          <h3>执行方式</h3>
          <span>选择采集方法及其必需参数</span>
        </header>
        <div v-if="editing" class="readonly-summary">
          <div class="readonly-summary__row">
            <span class="readonly-summary__label">执行任务类型</span>
            <a-tag color="arcoblue">{{ dataTypeLabel(editor.data_type) }}</a-tag>
          </div>
          <div class="readonly-summary__row">
            <span class="readonly-summary__label">{{
              taskFieldLabel(editor.data_type === "kline_resample" ? "target_frequency" : "frequency", "频率")
            }}</span>
            <span>{{ frequencyValue || "-" }}</span>
          </div>
        </div>
        <template v-else>
          <a-row :gutter="16">
            <a-col :span="12">
              <a-form-item field="data_type" label="执行任务类型" required>
                <a-select v-model="editor.data_type" placeholder="请选择执行任务类型" @change="onDataTypeChange">
                  <a-option v-for="config in availableDataTypeConfigs" :key="config.data_type" :value="config.data_type">{{
                    config.type_name
                  }}</a-option>
                </a-select>
              </a-form-item>
            </a-col>
          </a-row>
          <a-row :gutter="16">
            <a-col :span="12">
              <a-form-item
                field="frequency"
                :label="
                  taskFieldLabel(
                    editor.data_type === 'kline_resample' ? 'target_frequency' : 'frequency',
                    editor.data_type === 'kline_resample' ? '目标频率' : '采集频率'
                  )
                "
                required
              >
                <a-select v-if="frequencyOptions.length" v-model="frequencyValue" placeholder="请选择频率" allow-clear>
                  <a-option v-for="option in frequencyOptions" :key="option.value" :value="option.value">{{
                    option.label
                  }}</a-option>
                </a-select>
                <a-input
                  v-else
                  v-model="frequencyValue"
                  :placeholder="editor.data_type === 'kline_resample' ? '例如 45m、2h、1d' : '例如 1m、5m、1h'"
                  allow-clear
                />
                <span v-if="editor.data_type === 'kline_resample' && frequencyHints.length" class="field-hint">
                  可选：
                  <a-tag
                    v-for="item in frequencyHints"
                    :key="item"
                    size="small"
                    class="frequency-hint-tag"
                    :color="normalizedTargetFrequency === item ? 'arcoblue' : undefined"
                    @click="frequencyValue = item"
                  >
                    {{ item }}
                  </a-tag>
                </span>
              </a-form-item>
            </a-col>
          </a-row>
        </template>
      </section>

      <section class="editor-section">
        <header class="editor-section__header">
          <h3>采集范围</h3>
          <span>确定本任务要处理的标的或源数据</span>
        </header>
        <template v-if="editor.data_type === 'kline'">
          <div v-if="editing" class="readonly-summary">
            <div class="readonly-summary__row">
              <span class="readonly-summary__label">采集标的</span>
              <a-tag v-for="tag in editorSubjectTags" :key="tag.tag_id">
                {{ tag.tag_name || tag.tag_id }}
              </a-tag>
              <span v-if="!editorSubjectTags.length">-</span>
            </div>
            <span class="field-hint">Provider 和市场由标签固定绑定；已有任务不能增减另一个市场的标签，需要时请新建任务。</span>
          </div>
          <a-form-item v-else label="采集标的" required>
            <a-select
              v-model="subjectTagIds"
              placeholder="请选择一个或多个标签"
              :loading="loadingTags"
              allow-search
              allow-clear
              multiple
            >
              <a-option v-for="tag in tags" :key="tag.tag_id" :value="tag.tag_id">
                {{ tag.tag_name }}（{{ tagSource(tag) || "-" }} / {{ tagMarketType(tag) || "-" }}）
              </a-option>
            </a-select>
            <span class="field-hint">同一任务的标签必须来自同一来源；同时选现货、永续两个市场的标签时，按市场拆成两个任务。</span>
          </a-form-item>
          <a-alert v-if="!editing && crossMarketSelected" type="info" show-icon class="cross-market-hint"
            >选了现货、永续两个市场的标签：提交时按市场拆成两个任务，任务名后加市场名；同时用两个市场的策略各绑一个计算任务。</a-alert
          >
        </template>
        <a-alert v-else-if="editor.data_type !== 'kline_resample'" type="info" show-icon
          >当前执行任务类型没有标的范围，此项留空。</a-alert
        >
        <a-form-item v-if="!editing && editor.data_type === 'kline'" label="采集方式">
          <a-select v-model="ingest" placeholder="按市场默认">
            <a-option value="">按市场默认</a-option>
            <a-option v-for="option in INGEST_OPTIONS" :key="option.value" :value="option.value" :title="option.hint">{{
              option.label
            }}</a-option>
          </a-select>
          <span class="field-hint"
            >推送由 collector-stream 接收交易所推送；云函数拉取由云函数定时取数；本地拉取由 collector-stream
            定时取数。建好以后用命令行 collector ingest-mode set 切换。</span
          >
        </a-form-item>

        <template v-if="editor.data_type === 'kline_resample'">
          <div v-if="editing" class="readonly-summary">
            <div class="readonly-summary__row">
              <span class="readonly-summary__label">源采集任务</span>
              <span>{{ editingSourceTaskName }}</span>
            </div>
          </div>
          <a-form-item v-else label="源采集任务" required>
            <a-select v-model="sourceTaskId" placeholder="选择要合成的源采集任务" allow-search :loading="loadingSourceTasks">
              <a-option
                v-for="option in sourceTaskOptions"
                :key="option.task_id"
                :value="option.task_id"
                :disabled="Boolean(option.reason)"
              >
                {{ option.label }}<span v-if="option.reason" class="field-hint">（{{ option.reason }}）</span>
              </a-option>
            </a-select>
            <span class="field-hint">合成的源是已有的采集任务（或另一个合成任务）；源周期和序列标签由系统带出，不需要手填。</span>
          </a-form-item>
          <a-descriptions v-if="resamplePreview" :column="2" size="small" bordered class="resample-source-info">
            <a-descriptions-item label="源数据集">{{ resamplePreview.source_dataset_id || "-" }}</a-descriptions-item>
            <a-descriptions-item label="源周期">{{ resamplePreview.source_frequency || "-" }}</a-descriptions-item>
            <a-descriptions-item label="序列标签">{{ resamplePreview.source_series_tag || "-" }}</a-descriptions-item>
            <a-descriptions-item label="采集标的"
              >继承源任务，共 {{ resamplePreview.subject_count ?? 0 }} 个标的</a-descriptions-item
            >
            <a-descriptions-item v-if="resamplePreview.alignment" label="分桶方式" :span="2">
              {{ resamplePreview.alignment === "session" ? "按交易时段从开盘起切分" : "按 UTC 固定网格切分" }}
            </a-descriptions-item>
          </a-descriptions>
          <a-alert v-if="resampleError" type="error" show-icon class="resample-error">{{ resampleError }}</a-alert>
        </template>
      </section>

      <section class="editor-section editor-section--output">
        <header class="editor-section__header">
          <h3>输出字段</h3>
          <span>选择任务结果要保存的字段</span>
        </header>
        <div class="output-selection-summary">
          <a-button type="outline" @click="openOutputFieldPicker">管理输出字段</a-button>
          <strong>{{ selectedOutputFields.length }} 个字段</strong>
        </div>
        <div v-if="selectedOutputFieldLabels.length" class="selected-output-tags">
          <a-tag v-for="field in selectedOutputFieldLabels.slice(0, 6)" :key="field.id" color="arcoblue" :title="field.id">
            {{ field.name }}
          </a-tag>
          <span v-if="selectedOutputFieldLabels.length > 6" class="field-hint"
            >另有 {{ selectedOutputFieldLabels.length - 6 }} 个</span
          >
        </div>
        <span v-else class="field-hint">未选择时使用采集方法的默认输出字段。</span>
        <span v-if="editing" class="field-hint">输出字段只能减少，要新增请新建任务。</span>
      </section>

      <a-collapse v-if="!editing" :default-active-key="[]">
        <a-collapse-item key="advanced" header="高级设置">
          <a-form-item label="结果描述">
            <a-textarea v-model="resultConfig.description" :max-length="200" show-word-limit allow-clear />
          </a-form-item>
        </a-collapse-item>
      </a-collapse>
    </a-form>
    <template #footer>
      <a-space>
        <a-button @click="visible = false">取消</a-button>
        <a-button type="primary" :loading="submitLoading" @click="saveEditor">保存任务</a-button>
      </a-space>
    </template>
  </a-drawer>

  <OutputFieldPicker
    v-model:visible="outputFieldsVisible"
    v-model:selected="selectedOutputFields"
    :catalog="outputFieldCatalog"
    :available="availableExtraFields"
    :base="baseOutputFields"
    :editing="editing"
    :original-fields="originalOutputFields"
    :loading="loadingTypeFields"
    :load-error="typeFieldsLoadError"
    @retry="loadTaskTypeFields"
  />
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { storeToRefs } from "pinia";
import {
  CreateTask,
  UpdateTask,
  previewKlineResampleSource,
  type DataTypeFieldConfig,
  type ExtraFieldOption,
  type ResampleSourcePreview
} from "@/api/collector";
import { getDataset } from "@/api/storage/metadata";
import { useUserInfoStore } from "@/store/modules/user-info";
import { createSubmitScope } from "@/utils/submit-scope";
import { frequencyMinutes, normalizeFrequency } from "@/utils/frequency";
import { tagMarketType, tagSource } from "@/views/data/shared/catalog-attributes";
import {
  availableExtraFieldIds,
  baseOutputFieldIds,
  buildCollectionTaskParams,
  buildCollectionTaskPayload,
  collectionTaskNameError,
  INGEST_OPTIONS,
  isCrossMarketSelection,
  outputFieldLabels,
  parseCollectionTaskInput,
  planTaskGroups,
  pruneUnavailableFields,
  shouldDropUnavailableFields,
  splitTaskName,
  taskFrequency,
  taskTagSelectionError,
  type CollectionTaskDataType,
  type CollectionTaskInput,
  type CollectionTaskMarket,
  type CollectionTaskRecord,
  type OutputFieldEntry,
  type TaskEditorRequest,
  type TaskTagRoute
} from "./collection-task-params";
import { sourceCandidateReason } from "./resample-jobs";
import { useCollectionCatalog } from "./use-collection-catalog";
import OutputFieldPicker from "./output-field-picker.vue";

defineOptions({ name: "CollectionTaskEditorDrawer" });

type EditorForm = {
  task_id: string;
  task_name: string;
  description: string;
  space_id: string;
  data_type: CollectionTaskDataType | "";
  provider: string;
  market_type: CollectionTaskMarket;
  enabled: boolean;
  creator: string;
};

const props = defineProps<{ request?: TaskEditorRequest }>();
const visible = defineModel<boolean>("visible", { required: true });
const emit = defineEmits<{ (event: "saved"): void }>();

const catalog = useCollectionCatalog();
const { selectedSpaceId, availableDataTypeConfigs, tags, loadingTags, sourceTasks, loadingSourceTasks } = catalog;
const { dataTypeLabel, taskRouteSummary } = catalog;
const userInfoStore = useUserInfoStore();
const { account } = storeToRefs(userInfoStore);

const editorMode = ref<"create" | "edit">("create");
const editingRecord = ref<CollectionTaskRecord | null>(null);
const formRef = ref<{ validate?: () => Promise<unknown>; clearValidate?: () => void }>();
const submitLoading = ref(false);
const editor = reactive<EditorForm>({
  task_id: "",
  task_name: "",
  description: "",
  space_id: "",
  data_type: "",
  provider: "",
  market_type: "spot",
  enabled: true,
  creator: ""
});
const resultConfig = reactive({ description: "" });
const frequencyValue = ref("");
const subjectTagIds = ref<string[]>([]);
// 新建 K 线任务的采集方式；空表示按市场默认。
const ingest = ref("");
const sourceTaskId = ref("");
const resamplePreview = ref<ResampleSourcePreview | null>(null);
const resampleError = ref("");
const taskTypeFields = ref<DataTypeFieldConfig[]>([]);
const selectedOutputFields = ref<string[]>([]);
// 编辑已有任务时任务原来的输出字段：输出字段只能减少，不在这里面的不能勾。
const originalOutputFields = ref<string[]>([]);
// 接口给出的附加字段清单（随执行任务类型读取）；读到之前不能判断已选的附加字段是否还可选。
const extraFieldOptions = ref<ExtraFieldOption[]>([]);
// 字段清单（基础输出字段与附加字段）读到了才按它清理已选字段
const extraFieldOptionsLoaded = ref(false);
const baseOutputFields = ref<Set<string>>(new Set());
const outputFieldCatalog = ref<OutputFieldEntry[]>([]);
const outputFieldsVisible = ref(false);
const loadingTypeFields = ref(false);
const typeFieldsLoadError = ref(false);
let taskTypeFieldRequestToken = 0;

const editorTitle = computed(() => (editorMode.value === "create" ? "新建采集任务" : "修改采集任务"));
const editing = computed(() => editorMode.value === "edit");
const editorSubjectTags = computed(() => tags.value.filter(tag => subjectTagIds.value.includes(tag.tag_id)));
const selectedTagRoutes = computed<TaskTagRoute[]>(() =>
  editorSubjectTags.value.map(tag => ({ tag_id: tag.tag_id, source: tagSource(tag), market: tagMarketType(tag) }))
);
const crossMarketSelected = computed(() => isCrossMarketSelection(selectedTagRoutes.value));
// 编辑态合成任务的源任务只读展示：从源任务清单里找名字，找不到时退回任务 ID。
const editingSourceTaskName = computed(() => {
  const source = sourceTasks.value.find(task => task.task_id === sourceTaskId.value);
  return source ? source.task_name || source.task_id : sourceTaskId.value || "-";
});
const selectedOutputFieldLabels = computed(() => outputFieldLabels(selectedOutputFields.value, outputFieldCatalog.value));
const normalizedTargetFrequency = computed(() => normalizeFrequency(frequencyValue.value));
// 候选源任务：同空间已启用的采集任务与合成任务（合成任务可以再作为源逐级合成）；填了目标频率后，不能合成它的置灰并说明原因。
const sourceTaskOptions = computed(() =>
  sourceTasks.value
    .filter(task => task.enabled && task.task_id !== editor.task_id)
    .map(task => ({
      task_id: task.task_id,
      label: `${task.task_name || task.task_id} · ${taskFrequency(task)} · ${taskRouteSummary(task)}`,
      reason: sourceCandidateReason(taskFrequency(task), normalizedTargetFrequency.value)
    }))
);
// 预览给出的该市场可选目标周期：只列比源周期长的。
const frequencyHints = computed(() => {
  const source = frequencyMinutes(resamplePreview.value?.source_frequency || "");
  return (resamplePreview.value?.allowed_frequencies || []).filter(item => frequencyMinutes(item) > source);
});
const frequencyOptions = computed(() => {
  const field = taskTypeFields.value.find(item => item.field_key === "frequency");
  const configuredOptions = field?.field_options?.options;
  if (!Array.isArray(configuredOptions)) return [];
  return configuredOptions
    .map(value => (typeof value === "string" ? { value, label: value } : (value as { value?: unknown; label?: unknown })))
    .filter(option => option.value !== undefined && option.value !== null)
    .map(option => ({ value: String(option.value), label: String(option.label || option.value) }));
});

function taskFieldLabel(key: string, fallback: string): string {
  return taskTypeFields.value.find(field => field.field_key === key)?.field_name || fallback;
}

const rules = {
  task_name: [{ required: true, message: "请输入任务名称" }],
  data_type: [{ required: true, message: "请选择数据类型" }],
  enabled: [{ required: true, message: "请选择启用状态" }]
};

// 附加字段（资金费率、复权因子等）由接口给出适用的市场、品种和频率，默认不勾；这里算出当前条件下可选的那些。
const availableExtraFields = computed(() =>
  availableExtraFieldIds({
    dataType: editor.data_type,
    spaceId: editor.space_id,
    frequency: frequencyValue.value,
    routes: selectedTagRoutes.value,
    options: extraFieldOptions.value
  })
);
// 条件不再满足时去掉已选的附加字段。编辑已有任务时标签或附加字段清单可能还没读到，这时不动，避免误删。
const dropUnavailableFields = computed(() =>
  shouldDropUnavailableFields(editing.value, selectedTagRoutes.value.length, extraFieldOptionsLoaded.value)
);
// 同时监听已选字段：打开任务时判断值可能没变，已选字段换了也要按同一判断清理。
watch(
  [dropUnavailableFields, availableExtraFields, selectedOutputFields],
  ([drop, available]) => {
    const selected = pruneUnavailableFields(selectedOutputFields.value, available, drop, baseOutputFields.value);
    if (selected !== selectedOutputFields.value) selectedOutputFields.value = selected;
  },
  { immediate: true }
);

function resetEditor() {
  editor.task_id = "";
  editor.task_name = "";
  editor.description = "";
  editor.space_id = selectedSpaceId.value || "";
  editor.data_type = "";
  editor.provider = "";
  editor.market_type = "spot";
  editor.enabled = true;
  editor.creator = account.value?.user?.userName || "";
  frequencyValue.value = "";
  subjectTagIds.value = [];
  ingest.value = "";
  sourceTaskId.value = "";
  resamplePreview.value = null;
  resampleError.value = "";
  selectedOutputFields.value = [];
  originalOutputFields.value = [];
  taskTypeFields.value = [];
  resultConfig.description = "";
}

// 这次新建表单里已经建成的任务名：拆成两个任务时后一个失败，重试只补建没建成的。打开新的新建表单时清空。
const createdTaskNames = new Set<string>();

function openCreate() {
  editorMode.value = "create";
  createdTaskNames.clear();
  resetEditor();
}

function openEdit(record: CollectionTaskRecord) {
  const input = parseCollectionTaskInput(record);
  editingRecord.value = record;
  editorMode.value = "edit";
  editor.task_id = record.task_id;
  editor.task_name = record.task_name;
  editor.description = record.description;
  editor.space_id = record.space_id || selectedSpaceId.value || "";
  editor.data_type = input.dataType;
  editor.provider = input.provider;
  editor.market_type = input.market;
  editor.enabled = record.enabled;
  editor.creator = record.creator;
  frequencyValue.value = input.frequency;
  subjectTagIds.value = input.subjectTags || [];
  sourceTaskId.value = input.sourceTaskId || "";
  resampleError.value = "";
  // 已有合成任务：只读展示创建时由源任务推导并保存的源参数。
  resamplePreview.value =
    input.dataType === "kline_resample"
      ? {
          source_dataset_id: input.sourceId || "",
          source_frequency: input.sourceFrequency || "",
          source_series_tag: input.sourceSeriesTag || "",
          subject_count: 0,
          market_id: "",
          provider: input.provider,
          market_type: input.market,
          alignment: ""
        }
      : null;
  selectedOutputFields.value = input.outputFields || [];
  originalOutputFields.value = selectedOutputFields.value.slice();
  void loadTaskTypeFields();
  void loadTaskSubjectTags(record);
}

// 行操作“新建合成”：以这个任务作为源，打开新建表单。
function openResample(source: CollectionTaskRecord) {
  editorMode.value = "create";
  createdTaskNames.clear();
  resetEditor();
  editor.data_type = "kline_resample";
  sourceTaskId.value = source.task_id;
  void loadTaskTypeFields();
  void catalog.loadSourceTasks();
  scheduleResamplePreview();
}

watch(
  () => props.request,
  request => {
    if (!request) return;
    if (request.mode === "edit") openEdit(request.record);
    else if (request.mode === "resample") openResample(request.source);
    else openCreate();
  },
  { immediate: true }
);

function onDataTypeChange() {
  if (editing.value) return;
  editor.provider = "";
  frequencyValue.value = "";
  subjectTagIds.value = [];
  sourceTaskId.value = "";
  resamplePreview.value = null;
  resampleError.value = "";
  void loadTaskTypeFields();
  if (editor.data_type === "kline_resample") void catalog.loadSourceTasks();
}

function openOutputFieldPicker() {
  outputFieldsVisible.value = true;
}

async function loadTaskTypeFields() {
  const token = ++taskTypeFieldRequestToken;
  const dataType = editor.data_type;
  extraFieldOptions.value = [];
  extraFieldOptionsLoaded.value = false;
  baseOutputFields.value = new Set();
  outputFieldCatalog.value = [];
  taskTypeFields.value = [];
  typeFieldsLoadError.value = false;
  if (!dataType) return;
  loadingTypeFields.value = true;
  try {
    const detail = await catalog.loadDataTypeDetail(dataType);
    if (token !== taskTypeFieldRequestToken || editor.data_type !== dataType) return;
    taskTypeFields.value = [...(detail?.fields || [])].sort((a, b) => a.sort_order - b.sort_order);
    extraFieldOptions.value = detail?.extra_fields || [];
    baseOutputFields.value = baseOutputFieldIds(detail?.base_output_fields);
    outputFieldCatalog.value = catalog.outputFieldCatalogs.value[dataType] || [];
    extraFieldOptionsLoaded.value = true;
  } catch (error) {
    if (token === taskTypeFieldRequestToken) {
      typeFieldsLoadError.value = true;
      Message.warning(error instanceof Error ? error.message : "读取执行任务类型参数失败");
    }
  } finally {
    if (token === taskTypeFieldRequestToken) loadingTypeFields.value = false;
  }
}

function afterClose() {
  formRef.value?.clearValidate?.();
  visible.value = false;
}

function currentTaskInput(): CollectionTaskInput {
  if (!editor.data_type) throw new Error("请选择数据类型");
  const selectedTags = tags.value.filter(tag => subjectTagIds.value.includes(tag.tag_id));
  const firstTag = selectedTags[0];
  return {
    dataType: editor.data_type,
    provider: (firstTag && tagSource(firstTag)) || editor.provider,
    market: ((firstTag && tagMarketType(firstTag)) || editor.market_type) as CollectionTaskMarket,
    frequency: frequencyValue.value,
    subjectTags: subjectTagIds.value.slice(),
    sourceTaskId: sourceTaskId.value,
    ingest: ingest.value,
    // 提交前按条件再过滤一次：条件不满足的附加字段不带。
    outputFields: pruneUnavailableFields(
      selectedOutputFields.value,
      availableExtraFields.value,
      dropUnavailableFields.value,
      baseOutputFields.value
    )
  };
}

async function validateEditor() {
  const nameError = collectionTaskNameError(editor.task_name);
  if (nameError) {
    Message.error(nameError);
    return false;
  }
  if (!editor.space_id) {
    Message.error("请先选择空间");
    return false;
  }
  try {
    const input = currentTaskInput();
    if (input.dataType === "kline" && !subjectTagIds.value.length) {
      throw new Error("请选择标的标签");
    }
    if (input.dataType === "kline" && !editing.value) {
      const selectionError = taskTagSelectionError(selectedTagRoutes.value);
      if (selectionError) throw new Error(selectionError);
    }
    if (input.dataType === "kline_resample" && !editing.value) {
      if (!sourceTaskId.value.trim()) throw new Error("请选择源采集任务");
      if (resampleError.value) throw new Error(resampleError.value);
    }
    if (!frequencyValue.value.trim()) throw new Error("请输入采集频率");
    return true;
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "请完善任务配置");
    return false;
  }
}

// 抽屉底部「保存任务」：校验失败或提交失败时抽屉保持打开；保存成功后关闭并通知列表刷新。
async function saveEditor() {
  if (!(await validateEditor())) return;
  // 提交快照：预检等待期间用户可能取消并重新打开表单，旧提交只能用它自己的输入，
  // 不能把旧输入与新表单的任务名称、结果配置混合提交，也不能关闭新窗口。
  const submission = editorScope.begin();
  const editorSnapshot = { ...editor };
  const resultSnapshot = { ...resultConfig };
  submitLoading.value = true;
  try {
    if (editorMode.value === "create") {
      const input = currentTaskInput();
      const groups = input.dataType === "kline" ? planTaskGroups(selectedTagRoutes.value) : [{ suffix: "", tagIds: [] }];
      const created: string[] = [];
      for (const group of groups) {
        const groupInput = group.tagIds.length ? { ...input, subjectTags: group.tagIds } : input;
        // 上一次提交拆成两个任务、只建成了前一个：重试时跳过已建成的，只补建失败的那个（同名再建会冲突）
        const groupName = splitTaskName(editorSnapshot.task_name, group.suffix);
        if (createdTaskNames.has(groupName)) {
          created.push(groupName);
          continue;
        }
        const task = buildCollectionTaskPayload(
          groupInput,
          {
            task_name: groupName,
            description: editorSnapshot.description,
            space_id: editorSnapshot.space_id,
            creator: editorSnapshot.creator || account.value?.user?.userName || "",
            enabled: true
          },
          undefined
        );
        try {
          await CreateTask({
            task,
            result_config: { description: resultSnapshot.description.trim() }
          });
        } catch (error) {
          // 拆成两个任务时，后一个失败要保留已建好的前一个，并告诉用户补建哪个。
          if (created.length) {
            emit("saved");
            throw new Error(
              `已创建“${created.join("”“")}”，但“${task.task_name}”创建失败：${
                error instanceof Error ? error.message : "未知错误"
              }。修正后再点一次“保存任务”只会补建这个任务。`
            );
          }
          throw error;
        }
        created.push(task.task_name || "");
        createdTaskNames.add(groupName);
      }
      Message.success(`采集任务“${created.join("”“")}”已创建，将从下一采集周期开始生效`);
    } else {
      const input = currentTaskInput();
      const mutableTask = {
        task_id: editorSnapshot.task_id,
        task_name: editorSnapshot.task_name.trim(),
        description: editorSnapshot.description.trim(),
        enabled: editorSnapshot.enabled,
        collect_params: buildCollectionTaskParamsForUpdate(input)
      };
      await UpdateTask({
        space_id: editorSnapshot.space_id,
        task_id: editorSnapshot.task_id,
        task: mutableTask as Parameters<typeof UpdateTask>[0]["task"]
      });
      Message.success("变更成功，将从下一采集周期开始生效");
    }
    // 任务已保存；表单已被取消/重新打开或空间已切换时，不动新打开的窗口
    if (!submission.isCurrent()) return;
    visible.value = false;
    emit("saved");
  } catch (error) {
    if (!submission.isCurrent()) return;
    Message.error(error instanceof Error ? error.message : "保存任务失败");
  } finally {
    if (submission.isCurrent()) submitLoading.value = false;
  }
}

// 预览：选源任务或改目标频率后，向后端取推导出的源参数并按合成规则校验（防抖）。
let previewTimer: ReturnType<typeof setTimeout> | undefined;
let previewToken = 0;
function scheduleResamplePreview() {
  if (previewTimer) clearTimeout(previewTimer);
  previewTimer = setTimeout(() => void refreshResamplePreview(), 300);
}

async function refreshResamplePreview() {
  const token = ++previewToken;
  resampleError.value = "";
  if (!selectedSpaceId.value || !sourceTaskId.value) {
    resamplePreview.value = null;
    return;
  }
  try {
    const preview = await previewKlineResampleSource(selectedSpaceId.value, sourceTaskId.value, normalizedTargetFrequency.value);
    if (token !== previewToken) return;
    resamplePreview.value = preview;
    resampleError.value = preview.validation_error || "";
  } catch (error) {
    if (token !== previewToken) return;
    resamplePreview.value = null;
    resampleError.value = error instanceof Error ? error.message : "读取源任务失败";
  }
}

/** 任务没有记下标签时，按结果数据集的标的范围补齐。 */
async function loadTaskSubjectTags(record: CollectionTaskRecord) {
  if (record.tag_ids.length) {
    subjectTagIds.value = record.tag_ids.slice();
    return;
  }
  const datasetId = record.result?.dataset_id;
  if (!datasetId || !record.space_id) return;
  try {
    const dataset = await getDataset({ space_id: record.space_id, dataset_id: datasetId });
    subjectTagIds.value = (dataset.dataset?.subject_tags || []).slice();
  } catch (error) {
    Message.warning(error instanceof Error ? error.message : "读取任务标签失败");
  }
}

// 表单不编辑的几项（每函数标的数、允许迟到）原样带回，否则整体替换会把它们丢掉。
const PRESERVED_TASK_PARAM_KEYS = ["symbols_per_function", "allowed_lateness_seconds"];

function buildCollectionTaskParamsForUpdate(input: CollectionTaskInput) {
  const original = editingRecord.value?.collect_params ?? {};
  // 合成任务的参数创建后不可修改：更新时原样回传，只改名称、描述和启用状态。
  if (input.dataType === "kline_resample") return { ...original };
  const preserved: Record<string, unknown> = {};
  if (input.dataType === "kline") {
    for (const key of PRESERVED_TASK_PARAM_KEYS) {
      if (original[key] !== undefined) preserved[key] = original[key];
    }
  }
  return { ...preserved, ...buildCollectionTaskParams(input) };
}

// 选了源任务或改了目标频率：刷新预览与校验提示。
watch([sourceTaskId, normalizedTargetFrequency], () => {
  if (visible.value && editor.data_type === "kline_resample" && !editing.value) scheduleResamplePreview();
});

// 打开期间切了空间：表单属于旧空间，关掉并提示重新打开。
watch(selectedSpaceId, () => {
  if (!visible.value || editor.space_id === selectedSpaceId.value) return;
  visible.value = false;
  outputFieldsVisible.value = false;
  taskTypeFieldRequestToken += 1;
  Message.info("空间已切换，请重新打开采集任务窗口");
});

// 提交作用域（重新打开才使旧提交失效，@ok 自动关闭不影响正常保存）：编辑窗口打开/关闭都会使进行中的保存失效
const editorScope = createSubmitScope();
watch(visible, () => {
  editorScope.invalidate();
  submitLoading.value = false;
});
</script>

<style scoped>
.field-hint {
  display: block;
  margin-top: 4px;
  color: var(--color-text-3);
  font-size: 12px;
}
.editor-section {
  margin-bottom: 16px;
  padding: 16px 18px 2px;
  border: 1px solid var(--color-border-2);
  border-radius: 6px;
  background: var(--color-bg-2);
}
.editor-section__header {
  display: flex;
  align-items: baseline;
  gap: 10px;
  margin: 0 0 16px;
}
.editor-section__header h3 {
  margin: 0;
  color: var(--color-text-1);
  font-size: 14px;
  font-weight: 600;
}
.editor-section__header span {
  color: var(--color-text-3);
  font-size: 12px;
}
.output-selection-summary {
  display: flex;
  align-items: center;
  gap: 12px;
}
.output-selection-summary strong {
  color: var(--color-text-2);
  font-size: 13px;
  font-weight: 500;
}
.selected-output-tags {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  margin-top: 12px;
}
.immutable-hint {
  margin-bottom: var(--moox-space-4);
}
/* 编辑态不可变项的只读摘要：替代一排禁用控件，一眼可读 */
.readonly-summary {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 12px 14px;
  border: 1px solid var(--color-border-2);
  border-radius: 6px;
  background: var(--color-fill-1);
}
.readonly-summary__row {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
}
.readonly-summary__label {
  flex: none;
  min-width: 96px;
  color: var(--color-text-3);
  font-size: 13px;
}
.readonly-summary .field-hint {
  margin-top: 0;
}
@media (max-width: 640px) {
  .editor-section {
    padding: 12px 12px 0;
  }
  .editor-section__header {
    align-items: flex-start;
    flex-direction: column;
    gap: 3px;
    margin-bottom: 12px;
  }
}
</style>
