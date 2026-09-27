<template>
  <div class="moox-page">
    <a-spin :loading="loading">
      <div class="moox-inner">
        <a-space class="task-toolbar" wrap>
          <a-button type="primary" status="success" @click="onAdd">
            <template #icon><icon-plus /></template>
            新建采集任务
          </a-button>
          <a-input v-model="filters.taskId" placeholder="按内部任务 ID 筛选" allow-clear style="width: 180px" />
          <a-select v-model="filters.dataType" placeholder="数据类型" allow-clear style="width: 140px">
            <a-option v-for="config in availableDataTypeConfigs" :key="config.data_type" :value="config.data_type">
              {{ config.type_name }}
            </a-option>
          </a-select>
          <a-select v-model="filters.enabled" placeholder="启用状态" style="width: 120px">
            <a-option value="">全部</a-option>
            <a-option value="true">启用</a-option>
            <a-option value="false">禁用</a-option>
          </a-select>
          <a-button type="primary" @click="search">
            <template #icon><icon-search /></template>
            查询
          </a-button>
        </a-space>

        <a-table
          row-key="task_id"
          size="small"
          :data="taskList"
          :bordered="{ cell: true }"
          :loading="loading"
          :scroll="{ x: 1380 }"
          :pagination="paginationConfig"
          @page-change="onPageChange"
          @page-size-change="onPageSizeChange"
        >
          <template #columns>
            <a-table-column title="任务名称" data-index="task_name" :width="240">
              <template #cell="{ record }">
                <a-link @click="onViewDetails(record)">{{ record.task_name || record.task_id }}</a-link>
              </template>
            </a-table-column>
            <a-table-column title="数据类型" :width="120">
              <template #cell="{ record }">{{ dataTypeLabel(record.data_type) }}</template>
            </a-table-column>
            <a-table-column title="标签" :width="220">
              <template #cell="{ record }">{{ taskTagSummary(record) }}</template>
            </a-table-column>
            <a-table-column title="来源 / 市场" :width="180">
              <template #cell="{ record }">{{ taskRouteSummary(record) }}</template>
            </a-table-column>
            <a-table-column title="频率" :width="100">
              <template #cell="{ record }">{{ taskFrequency(record) }}</template>
            </a-table-column>
            <a-table-column title="结果状态" :width="120" align="center">
              <template #cell="{ record }">
                <a-tag size="small" :color="resultStatusColor(record)">{{ resultStatusLabel(record) }}</a-tag>
              </template>
            </a-table-column>
            <a-table-column title="最近数据时间" :width="180">
              <template #cell="{ record }">{{ formatDateTime(record.result?.last_data_time) }}</template>
            </a-table-column>
            <a-table-column title="启用状态" :width="100" align="center">
              <template #cell="{ record }">
                <a-tag size="small" :color="record.enabled ? 'green' : 'orange'">
                  {{ record.enabled ? "启用" : "禁用" }}
                </a-tag>
              </template>
            </a-table-column>
            <a-table-column title="操作" :width="260" align="center" fixed="right">
              <template #cell="{ record }">
                <a-space wrap>
                  <a-button
                    size="mini"
                    :status="record.enabled ? 'warning' : 'success'"
                    @click="handleEnableChange(record, !record.enabled)"
                  >
                    {{ record.enabled ? "禁用" : "启用" }}
                  </a-button>
                  <a-button v-if="record.data_type === 'kline_resample'" type="text" size="mini" @click="openBackfill(record)">
                    <template #icon><icon-refresh /></template>
                    回填
                  </a-button>
                  <a-button type="primary" size="mini" @click="onUpdate(record)">
                    <template #icon><icon-edit /></template>
                    修改
                  </a-button>
                  <a-button status="danger" type="text" size="mini" @click="openDelete(record)">删除</a-button>
                </a-space>
              </template>
            </a-table-column>
          </template>
        </a-table>
      </div>
    </a-spin>

    <a-modal
      v-model:visible="editorVisible"
      :title="editorTitle"
      ok-text="保存任务"
      width="860px"
      :ok-loading="submitLoading"
      @before-ok="handleOk"
      @close="afterClose"
      @cancel="afterClose"
    >
      <a-form ref="formRef" auto-label-width :rules="rules" :model="editor" layout="vertical">
        <a-alert v-if="editing" type="info" show-icon class="immutable-hint">
          数据类型、标签范围、结果身份和结果形态不可修改，如需变更请创建新任务。
        </a-alert>
        <section class="editor-section">
          <header class="editor-section__header"><h3>任务信息</h3><span>用于识别和说明这项采集任务</span></header>
          <a-form-item field="task_name" label="任务名称" validate-trigger="blur">
            <a-input v-model="editor.task_name" placeholder="例如：币安现货 1 小时行情" allow-clear :max-length="80" show-word-limit />
          </a-form-item>
          <a-form-item field="description" label="描述">
            <a-textarea v-model="editor.description" placeholder="补充任务用途、数据范围或负责人信息" :max-length="200" show-word-limit allow-clear />
          </a-form-item>
          <a-row v-if="editing" :gutter="16">
            <a-col :span="12">
              <a-form-item field="enabled" label="启用状态" required>
                <a-select v-model="editor.enabled"><a-option :value="true">启用</a-option><a-option :value="false">禁用</a-option></a-select>
              </a-form-item>
            </a-col>
            <a-col :span="12">
              <a-form-item label="任务 ID"><a-input :model-value="editor.task_id" disabled /></a-form-item>
            </a-col>
          </a-row>
          <a-form-item v-if="editing" label="创建人"><a-input :model-value="editor.creator || '-'" disabled /></a-form-item>
        </section>

        <section class="editor-section">
          <header class="editor-section__header"><h3>执行方式</h3><span>选择采集方法及其必需参数</span></header>
          <a-row :gutter="16">
            <a-col :span="12">
              <a-form-item field="data_type" label="执行任务类型" required>
                <a-select v-model="editor.data_type" placeholder="请选择执行任务类型" :disabled="editing" @change="onDataTypeChange">
                  <a-option v-for="config in availableDataTypeConfigs" :key="config.data_type" :value="config.data_type">{{ config.type_name }}</a-option>
                </a-select>
              </a-form-item>
            </a-col>
          </a-row>
          <a-row :gutter="16">
            <a-col :span="12">
              <a-form-item field="frequency" :label="taskFieldLabel(editor.data_type === 'kline_resample' ? 'target_frequency' : 'intervals', editor.data_type === 'kline_resample' ? '目标频率' : '采集频率')" required>
                <a-select v-if="frequencyOptions.length" v-model="frequencyValue" placeholder="请选择频率" allow-clear :disabled="editing">
                  <a-option v-for="option in frequencyOptions" :key="option.value" :value="option.value">{{ option.label }}</a-option>
                </a-select>
                <a-input v-else v-model="frequencyValue" placeholder="例如 1m、5m、1h" allow-clear :disabled="editing" />
              </a-form-item>
            </a-col>
          </a-row>
        </section>

        <section class="editor-section">
          <header class="editor-section__header"><h3>采集范围</h3><span>确定本任务要处理的标的或源数据</span></header>
          <a-form-item v-if="editor.data_type === 'kline' || editor.data_type === 'kline_resample'" label="采集标的" :required="editor.data_type === 'kline'">
            <a-select v-model="subjectTagIds" placeholder="请选择一个或多个标签" :loading="loadingTags" allow-search allow-clear multiple :disabled="editing">
              <a-option v-for="tag in tags" :key="tag.tag_id" :value="tag.tag_id">
                {{ tag.tag_name }}（{{ tag.source || "-" }} / {{ tag.market_type || "-" }}）
              </a-option>
            </a-select>
            <span class="field-hint">Provider 和市场由标签固定绑定；一个任务可选择不同来源/市场的多个标签。</span>
          </a-form-item>
          <a-alert v-else type="info" show-icon>当前执行任务类型没有标的范围，此项留空。</a-alert>

          <template v-if="editor.data_type === 'kline_resample'">
            <a-form-item :label="taskFieldLabel('source_dataset_id', '源行情')" required>
              <a-select v-model="sourceIdValue" placeholder="请选择源行情" @change="loadSourceSubjectTags" :loading="loadingSources" allow-search allow-clear :disabled="editing">
                <a-option v-for="source in resampleSourceOptions" :key="source.source_id" :value="source.source_id">{{ source.name || "源行情" }}</a-option>
              </a-select>
            </a-form-item>
            <a-row :gutter="16">
              <a-col :span="12"><a-form-item :label="taskFieldLabel('source_frequency', '源周期')" required><a-input v-model="sourceFrequencyValue" placeholder="例如 1m、5m、1h" allow-clear :disabled="editing" /></a-form-item></a-col>
              <a-col :span="12"><a-form-item :label="taskFieldLabel('source_series_tag', '序列标签')" required><a-input v-model="sourceSeriesTagValue" placeholder="例如 venue:binance" allow-clear :disabled="editing" /></a-form-item></a-col>
            </a-row>
            <a-form-item :label="taskFieldLabel('settle_delay_ms', '收盘等待（毫秒）')">
              <a-input-number v-model="settleDelayMSValue" :min="0" :max="86400000" :precision="0" placeholder="留空使用默认值" allow-clear :disabled="editing" style="width: 100%" />
            </a-form-item>
          </template>
        </section>

        <section v-if="!editing" class="editor-section editor-section--output">
          <header class="editor-section__header"><h3>输出字段</h3><span>选择任务结果要保存的字段</span></header>
          <div class="output-selection-summary">
            <a-button type="outline" @click="openOutputFieldPicker">管理输出字段</a-button>
            <strong>{{ selectedOutputFields.length }} 个字段</strong>
          </div>
          <div v-if="selectedOutputFieldLabels.length" class="selected-output-tags">
            <a-tag v-for="field in selectedOutputFieldLabels.slice(0, 6)" :key="field.id" color="arcoblue" :title="field.id">
              {{ field.name }}
            </a-tag>
            <span v-if="selectedOutputFieldLabels.length > 6" class="field-hint">另有 {{ selectedOutputFieldLabels.length - 6 }} 个</span>
          </div>
          <span v-else class="field-hint">未选择时使用采集方法的默认输出字段。</span>
        </section>

        <a-collapse v-if="!editing" :default-active-key="[]">
          <a-collapse-item key="advanced" header="高级设置">
            <a-form-item label="结果 DataNode">
              <a-select
                v-model="resultConfig.data_node_id"
                placeholder="留空使用系统默认节点"
                :loading="loadingDataNodes"
                allow-search
                allow-clear
              >
                <a-option v-for="node in dataNodes" :key="node.node_id" :value="node.node_id">
                  {{ node.name || node.node_id }}
                </a-option>
              </a-select>
            </a-form-item>
            <a-form-item label="保留时长">
              <a-input v-model="resultConfig.keep_duration" placeholder="例如 30d，0 表示不限制" allow-clear />
            </a-form-item>
            <a-form-item label="结果描述">
              <a-textarea v-model="resultConfig.description" :max-length="200" show-word-limit allow-clear />
            </a-form-item>
          </a-collapse-item>
        </a-collapse>
      </a-form>
    </a-modal>

    <a-modal v-model:visible="outputFieldsVisible" title="选择输出字段" width="900px" ok-text="确认选择" :ok-loading="loadingOutputFields" @before-ok="confirmOutputFieldSelection" @cancel="cancelOutputFieldSelection" @close="cancelOutputFieldSelection">
      <a-alert type="info" show-icon>展示当前空间全部分类的启用字段，可按需选择输出字段。</a-alert>
      <a-alert v-if="outputFieldLoadError" class="output-field-error" type="error" show-icon>
        字段目录加载失败 <a-link @click="loadOutputFieldCatalog(true)">重试</a-link>
      </a-alert>
      <a-spin :loading="loadingOutputFields" class="output-field-picker-spin">
        <div class="output-field-toolbar">
          <a-input v-model="outputFieldKeyword" allow-clear placeholder="搜索字段名称、ID 或描述" />
          <span>{{ outputFieldDraft.length }} 个已选择</span>
        </div>
        <div class="output-field-sections">
          <section v-for="section in visibleOutputFieldSections" :key="section.id" class="output-field-section">
            <header class="output-field-section__header">
              <div><strong>{{ section.name }}</strong><span>{{ section.fields.length }} 个字段</span></div>
              <a-button size="mini" type="text" @click="toggleOutputFieldSection(section.fields)">
                {{ section.fields.every(field => outputFieldDraft.includes(field.field_id)) ? (outputFieldKeyword ? "取消匹配项" : "取消本组") : (outputFieldKeyword ? "选择匹配项" : "选择本组") }}
              </a-button>
            </header>
            <div class="output-field-grid">
              <label v-for="field in section.fields" :key="field.field_id" class="output-field-option" :title="field.description || field.field_id">
                <a-checkbox :model-value="outputFieldDraft.includes(field.field_id)" @change="setOutputField(field.field_id, $event)" />
                <span class="output-field-option__text"><strong>{{ field.name }}</strong><code>{{ field.field_id }}</code></span>
              </label>
            </div>
          </section>
          <a-empty v-if="!loadingOutputFields && !visibleOutputFieldSections.length" :description="outputFieldKeyword ? '没有匹配的字段' : '当前空间暂无启用字段'" />
        </div>
      </a-spin>
    </a-modal>

    <a-modal v-model:visible="detailVisible" title="任务详情" :footer="false" width="800px">
      <a-descriptions v-if="detailData" :column="2" bordered>
        <a-descriptions-item label="任务 ID">{{ detailData.task_id }}</a-descriptions-item>
        <a-descriptions-item label="任务名称">{{ detailData.task_name || "-" }}</a-descriptions-item>
        <a-descriptions-item label="数据类型">{{ dataTypeLabel(detailData.data_type) }}</a-descriptions-item>
        <a-descriptions-item label="标签">{{ taskTagSummary(detailData) }}</a-descriptions-item>
        <a-descriptions-item label="来源 / 市场">{{ taskRouteSummary(detailData) }}</a-descriptions-item>
        <a-descriptions-item label="频率">{{ taskFrequency(detailData) }}</a-descriptions-item>
        <a-descriptions-item label="结果状态">{{ resultStatusLabel(detailData) }}</a-descriptions-item>
        <a-descriptions-item label="最近数据时间">{{ formatDateTime(detailData.result?.last_data_time) }}</a-descriptions-item>
        <a-descriptions-item label="启用状态">{{ detailData.enabled ? "启用" : "禁用" }}</a-descriptions-item>
        <a-descriptions-item label="创建人">{{ detailData.creator || "-" }}</a-descriptions-item>
        <a-descriptions-item label="创建时间">{{ formatDateTime(detailData.create_time) }}</a-descriptions-item>
        <a-descriptions-item label="修改时间">{{ formatDateTime(detailData.modify_time) }}</a-descriptions-item>
      </a-descriptions>
      <a-divider />
      <a-descriptions v-if="detailData" :column="1" bordered>
        <a-descriptions-item label="任务描述">{{ detailData.description || "-" }}</a-descriptions-item>
        <a-descriptions-item v-if="detailData.last_error" label="准备信息">
          {{ detailData.last_error }}
        </a-descriptions-item>
      </a-descriptions>
    </a-modal>

    <a-modal v-model:visible="deleteVisible" title="删除采集任务" :ok-loading="deleteLoading" @before-ok="handleDeleteOk">
      <a-alert type="warning" show-icon>
        删除任务会移除任务配置、实例和运行记录。结果数据默认保留；如需物理删除，请显式选择第二项。
      </a-alert>
      <a-radio-group v-model="deleteResultData" direction="vertical" class="delete-options">
        <a-radio :value="false">删除任务，保留结果数据</a-radio>
        <a-radio :value="true">删除任务并物理删除结果数据和数据视图</a-radio>
      </a-radio-group>
    </a-modal>

    <ResampleBackfillDialog
      v-if="backfillTarget"
      v-model:visible="backfillVisible"
      :space-id="selectedSpaceId || ''"
      :task-id="backfillTarget.taskId"
      :target-frequency="backfillTarget.targetFrequency"
      :source-keep-duration="backfillTarget.sourceKeepDuration"
      :active-backfill="activeBackfill"
      @started="onBackfillChanged"
      @cancelled="onBackfillChanged"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from "vue";
import { useRoute } from "vue-router";
import { Message } from "@arco-design/web-vue";
import {
  CreateTask,
  DeleteTask,
  DisableTask,
  GetDataTypeConfigs,
  GetDataTypeConfigWithFields,
  GetTaskList,
  UpdateTask,
  getKlineResampleBackfillStatus,
  type DataTypeConfig
} from "@/api/collector";
import { getDataset, getView, listDataNodes, listDatasets, listFieldGroups, listFields, listTags } from "@/api/storage/metadata";
import type { DataNode, Field, FieldGroup, Tag } from "@/api/storage/types";
import { useSpaceStore } from "@/store/modules/space";
import { useUserInfoStore } from "@/store/modules/user-info";
import { storeToRefs } from "pinia";
import {
  buildCollectionTaskParams,
  buildCollectionTaskPayload,
  collectionSourceMatches,
  collectionTaskNameError,
  normalizeCollectionTask,
  parseCollectionTaskInput,
  taskFrequency,
  type CollectionSourceOption,
  type CollectionTaskDataType,
  type CollectionTaskInput,
  type CollectionTaskMarket,
  type CollectionTaskRecord
} from "./collection-task-params";
import ResampleBackfillDialog from "./resample-backfill.vue";
import type { ResampleBackfillSummary } from "./resample-backfill";
import { groupPath } from "@/views/data/fields/field-workbench";

type EnabledFilter = "" | "true" | "false";
type EditorMode = "create" | "edit";
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

const loading = ref(false);
const submitLoading = ref(false);
const loadingSources = ref(false);
const loadingTags = ref(false);
const loadingTypeFields = ref(false);
const loadingDataNodes = ref(false);
const taskList = ref<CollectionTaskRecord[]>([]);
const dataTypeConfigs = ref<DataTypeConfig[]>([]);
const sourceOptions = ref<CollectionSourceOption[]>([]);
const tags = ref<Tag[]>([]);
const taskTypeFields = ref<import("@/api/collector").DataTypeFieldConfig[]>([]);
const dataNodes = ref<DataNode[]>([]);
const editorVisible = ref(false);
const editorMode = ref<EditorMode>("create");
const formRef = ref<{ validate?: () => Promise<unknown>; clearValidate?: () => void }>();
const detailVisible = ref(false);
const detailData = ref<CollectionTaskRecord>();
const deleteVisible = ref(false);
const deleteLoading = ref(false);
const deleteResultData = ref(false);
const deleteTarget = ref<CollectionTaskRecord>();
const backfillVisible = ref(false);
const activeBackfill = ref<ResampleBackfillSummary | null>(null);
const backfillTarget = ref<{ taskId: string; targetFrequency: string; sourceKeepDuration: string }>();

const frequencyValue = ref("");
const subjectTagIds = ref<string[]>([]);
const selectedOutputFields = ref<string[]>([]);
const outputFieldDraft = ref<string[]>([]);
const outputFieldsVisible = ref(false);
const outputFieldCatalog = ref<Field[]>([]);
const outputFieldGroups = ref<FieldGroup[]>([]);
const outputFieldKeyword = ref("");
const loadingOutputFields = ref(false);
const outputFieldLoadError = ref(false);
const outputCatalogSpaceID = ref("");
let outputCatalogRequestToken = 0;
let tagListRequestToken = 0;
let taskTypeFieldRequestToken = 0;
const outputFieldSections = computed(() => {
  const grouped = new Map<string, Field[]>();
  for (const field of outputFieldCatalog.value) {
    const groupID = field.group_id || "";
    const list = grouped.get(groupID) || [];
    list.push(field);
    grouped.set(groupID, list);
  }
  const allGroupIDs = new Set(outputFieldGroups.value.map(group => group.group_id));
  const activeGroups = outputFieldGroups.value.filter(group => group.status === "active");
  const sections = activeGroups
    .filter(group => grouped.has(group.group_id))
    .map(group => ({ id: group.group_id, name: groupPath(outputFieldGroups.value, group.group_id), fields: grouped.get(group.group_id) || [] }))
    .sort((a, b) => a.name.localeCompare(b.name, "zh-CN"));
  const knownGroups = allGroupIDs;
  for (const [groupID, fields] of grouped) {
    if (groupID && !knownGroups.has(groupID)) sections.push({ id: groupID, name: groupID, fields });
  }
  const ungrouped = grouped.get("") || [];
  if (ungrouped.length) sections.push({ id: "ungrouped", name: "未分组字段", fields: ungrouped });
  return sections;
});
const visibleOutputFieldSections = computed(() => {
  const keyword = outputFieldKeyword.value.trim().toLocaleLowerCase();
  if (!keyword) return outputFieldSections.value;
  return outputFieldSections.value
    .map(section => ({
      ...section,
      fields: section.fields.filter(field => `${field.name} ${field.field_id} ${field.description || ""}`.toLocaleLowerCase().includes(keyword))
    }))
    .filter(section => section.fields.length > 0);
});
const selectedOutputFieldLabels = computed(() => selectedOutputFields.value.map(id => {
  const field = outputFieldCatalog.value.find(item => item.field_id === id);
  return { id, name: field?.name || id };
}));
const sourceIdValue = ref("");
const sourceFrequencyValue = ref("");
const sourceSeriesTagValue = ref("");
const settleDelayMSValue = ref<number>();
const resultConfig = reactive({
  data_node_id: "",
  keep_duration: "0",
  description: ""
});

const filters = reactive<{
  taskId: string;
  dataType: string;
  enabled: EnabledFilter;
}>({
  taskId: "",
  dataType: "",
  enabled: ""
});

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

const pagination = reactive({
  current: 1,
  pageSize: 10,
  total: 0
});

const spaceStore = useSpaceStore();
const { selectedSpaceId } = storeToRefs(spaceStore);
const route = useRoute();
const userInfoStore = useUserInfoStore();
const { account } = storeToRefs(userInfoStore);

const editorTitle = computed(() => (editorMode.value === "create" ? "新建采集任务" : "修改采集任务"));
const editing = computed(() => editorMode.value === "edit");
const availableDataTypeConfigs = computed(() =>
  dataTypeConfigs.value.filter(config => !["instrument", "symbol"].includes(config.data_type))
);
const resampleSourceOptions = computed(() =>
  sourceOptions.value.filter(source =>
    collectionSourceMatches(source, editor.provider, "kline_resample", editor.market_type, sourceFrequencyValue.value)
  )
);
const frequencyOptions = computed(() => {
  const field = taskTypeFields.value.find(item => item.field_key === "intervals");
  const configuredOptions = field?.field_options?.options;
  if (!Array.isArray(configuredOptions)) return [];
  return configuredOptions.map(value => typeof value === "string" ? { value, label: value } : value as { value?: unknown; label?: unknown })
    .filter(option => option.value !== undefined && option.value !== null)
    .map(option => ({ value: String(option.value), label: String(option.label || option.value) }));
});

function taskFieldLabel(key: string, fallback: string): string {
  return taskTypeFields.value.find(field => field.field_key === key)?.field_name || fallback;
}

const paginationConfig = computed(() => ({
  current: pagination.current,
  pageSize: pagination.pageSize,
  total: pagination.total,
  showTotal: true,
  showPageSize: true
}));

const rules = {
  task_name: [{ required: true, message: "请输入任务名称" }],
  data_type: [{ required: true, message: "请选择数据类型" }],
  enabled: [{ required: true, message: "请选择启用状态" }]
};

function dataTypeLabel(value?: string) {
  return dataTypeConfigs.value.find(config => config.data_type === value)?.type_name || value || "-";
}

function tagsForTask(task: CollectionTaskRecord): Tag[] {
  const ids = new Set(task.tag_ids || []);
  return tags.value.filter(tag => ids.has(tag.tag_id));
}

function taskTagSummary(task: CollectionTaskRecord): string {
  const matched = tagsForTask(task);
  if (matched.length) return matched.map(tag => tag.tag_name || tag.tag_id).join("、");
  return (task.tag_ids || []).join("、") || "-";
}

function taskRouteSummary(task: CollectionTaskRecord): string {
  const routes = [...new Set(tagsForTask(task).map(tag => `${tag.source || "-"} / ${tag.market_type || "-"}`))];
  if (routes.length) return routes.join("；");
  if (task.data_type === "kline_resample" && (task.provider || task.market_type)) return `${task.provider || "-"} / ${task.market_type || "-"}`;
  return "-";
}

function formatDateTime(value?: string) {
  if (!value) return "-";
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? value : date.toLocaleString("zh-CN");
}

function resultStatusLabel(task: CollectionTaskRecord) {
  const status = String(task.result?.status || "").toLowerCase();
  const prepareState = String(task.prepare_state || "").toLowerCase();
  if (status === "error" || prepareState === "error" || task.last_error) return "结果异常";
  if (task.result?.view_id && ["active", "ready", "succeeded"].includes(status)) return "结果可用";
  return "结果准备中";
}

function resultStatusColor(task: CollectionTaskRecord) {
  const label = resultStatusLabel(task);
  return label === "结果可用" ? "green" : label === "结果异常" ? "red" : "orange";
}

function selectEnabledFilter(): boolean | undefined {
  if (filters.enabled === "true") return true;
  if (filters.enabled === "false") return false;
  return undefined;
}

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
  sourceIdValue.value = "";
  sourceFrequencyValue.value = "";
  sourceSeriesTagValue.value = "";
  settleDelayMSValue.value = undefined;
  selectedOutputFields.value = [];
  taskTypeFields.value = [];
  resultConfig.data_node_id = "";
  resultConfig.keep_duration = "0";
  resultConfig.description = "";
}

function onAdd() {
  editorMode.value = "create";
  resetEditor();
  editorVisible.value = true;
}

function onUpdate(record: CollectionTaskRecord) {
  const input = parseCollectionTaskInput(record);
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
  sourceIdValue.value = input.sourceId || "";
  sourceFrequencyValue.value = input.sourceFrequency || "";
  sourceSeriesTagValue.value = input.sourceSeriesTag || "";
  settleDelayMSValue.value = input.settleDelayMS;
  selectedOutputFields.value = input.outputFields || [];
  outputFieldDraft.value = selectedOutputFields.value.slice();
  editorVisible.value = true;
  void loadTaskTypeFields();
  void loadTaskSubjectTags(record);
}

function onDataTypeChange() {
  if (editing.value) return;
  editor.provider = "";
  frequencyValue.value = "";
  subjectTagIds.value = [];
  sourceIdValue.value = "";
  sourceFrequencyValue.value = "";
  sourceSeriesTagValue.value = "";
  settleDelayMSValue.value = undefined;
  void loadTaskTypeFields();
}

function openOutputFieldPicker() {
  outputFieldKeyword.value = "";
  outputFieldDraft.value = selectedOutputFields.value.slice();
  outputFieldsVisible.value = true;
  void loadOutputFieldCatalog();
}

async function loadOutputFieldCatalog(force = false) {
  const spaceID = selectedSpaceId.value;
  const token = ++outputCatalogRequestToken;
  if (!spaceID) {
    outputFieldCatalog.value = [];
    outputFieldGroups.value = [];
    outputCatalogSpaceID.value = "";
    outputFieldLoadError.value = false;
    return;
  }
  if (!force && outputCatalogSpaceID.value === spaceID) return;
  loadingOutputFields.value = true;
  outputFieldLoadError.value = false;
  try {
    const groups: FieldGroup[] = [];
    for (let page = 1; ; page += 1) {
      const response = await listFieldGroups({ space_id: spaceID, page: { page, size: 200 } });
      if (token !== outputCatalogRequestToken || spaceID !== selectedSpaceId.value) return;
      groups.push(...(response.field_groups || []));
      if (!response.page_result?.has_more || !(response.field_groups || []).length) break;
    }
    const fields: Field[] = [];
    for (let page = 1; ; page += 1) {
      const response = await listFields({ space_id: spaceID, status: "active", sort_by: "sort_order", sort_order: "asc", page: { page, size: 500 } });
      if (token !== outputCatalogRequestToken || spaceID !== selectedSpaceId.value) return;
      fields.push(...(response.fields || []));
      if (!response.page_result?.has_more || !(response.fields || []).length) break;
    }
    if (token !== outputCatalogRequestToken || spaceID !== selectedSpaceId.value) return;
    const allGroupIDs = new Set(groups.map(group => group.group_id));
    const activeGroupIDs = new Set(groups.filter(group => group.status === "active").map(group => group.group_id));
    outputFieldGroups.value = groups;
    outputFieldCatalog.value = fields.filter(field => !field.group_id || activeGroupIDs.has(field.group_id) || !allGroupIDs.has(field.group_id));
    outputCatalogSpaceID.value = spaceID;
  } catch (error) {
    if (token === outputCatalogRequestToken) {
      outputFieldLoadError.value = true;
      outputCatalogSpaceID.value = "";
      Message.warning(error instanceof Error ? error.message : "读取字段目录失败");
    }
  } finally {
    if (token === outputCatalogRequestToken) loadingOutputFields.value = false;
  }
}

function setOutputField(fieldID: string, checked: boolean | string | number) {
  const selected = Boolean(checked);
  outputFieldDraft.value = selected
    ? [...new Set([...outputFieldDraft.value, fieldID])]
    : outputFieldDraft.value.filter(id => id !== fieldID);
}

function toggleOutputFieldSection(fields: Field[]) {
  const fieldIDs = new Set(fields.map(field => field.field_id));
  const allSelected = [...fieldIDs].every(id => outputFieldDraft.value.includes(id));
  outputFieldDraft.value = allSelected
    ? outputFieldDraft.value.filter(id => !fieldIDs.has(id))
    : [...new Set([...outputFieldDraft.value, ...fieldIDs])];
}

function confirmOutputFieldSelection() {
  selectedOutputFields.value = outputFieldDraft.value.slice();
  outputFieldsVisible.value = false;
  return true;
}

function cancelOutputFieldSelection() {
  outputFieldDraft.value = selectedOutputFields.value.slice();
}

async function loadTaskTypeFields() {
  const token = ++taskTypeFieldRequestToken;
  const dataType = editor.data_type;
  if (!dataType) { taskTypeFields.value = []; return; }
  taskTypeFields.value = [];
  loadingTypeFields.value = true;
  try {
    const response = await GetDataTypeConfigWithFields(dataType);
    if (token !== taskTypeFieldRequestToken || editor.data_type !== dataType) return;
    taskTypeFields.value = [...(response.detail?.fields || [])].sort((a, b) => a.sort_order - b.sort_order);
  } catch (error) {
    if (token === taskTypeFieldRequestToken) {
      taskTypeFields.value = [];
      Message.warning(error instanceof Error ? error.message : "读取执行任务类型参数失败");
    }
  } finally { if (token === taskTypeFieldRequestToken) loadingTypeFields.value = false; }
}

function afterClose() {
  formRef.value?.clearValidate?.();
  editorVisible.value = false;
}

function currentTaskInput(): CollectionTaskInput {
  if (!editor.data_type) throw new Error("请选择数据类型");
  const selectedTags = tags.value.filter(tag => subjectTagIds.value.includes(tag.tag_id));
  const firstTag = selectedTags[0];
  return {
    dataType: editor.data_type,
    provider: firstTag?.source || editor.provider,
    market: (firstTag?.market_type || editor.market_type) as CollectionTaskMarket,
    frequency: frequencyValue.value,
    subjectTags: subjectTagIds.value.slice(),
    sourceId: sourceIdValue.value,
    sourceFrequency: sourceFrequencyValue.value,
    sourceSeriesTag: sourceSeriesTagValue.value,
    settleDelayMS: settleDelayMSValue.value,
    outputFields: selectedOutputFields.value
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
    if (input.dataType === "kline_resample" && !sourceIdValue.value.trim()) {
      throw new Error("请选择源行情");
    }
    if (input.dataType === "kline_resample" && !sourceFrequencyValue.value.trim()) {
      throw new Error("请输入源周期");
    }
    if (input.dataType === "kline_resample" && !sourceSeriesTagValue.value.trim()) {
      throw new Error("请输入序列标签");
    }
    if (!frequencyValue.value.trim()) throw new Error("请输入采集频率");
    return true;
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "请完善任务配置");
    return false;
  }
}

async function handleOk(): Promise<boolean> {
  if (!(await validateEditor())) return false;
  submitLoading.value = true;
  try {
    if (editorMode.value === "create") {
      const input = currentTaskInput();
      if (!input.subjectTags?.length && input.dataType === "kline_resample" && input.sourceId) {
        const sourceDataset = await getDataset({ space_id: editor.space_id, dataset_id: input.sourceId });
        input.subjectTags = (sourceDataset.dataset?.subject_tags || []).slice();
        subjectTagIds.value = input.subjectTags;
      }
      const task = buildCollectionTaskPayload(
        input,
        {
          task_name: editor.task_name,
          description: editor.description,
          space_id: editor.space_id,
          creator: editor.creator || account.value?.user?.userName || "",
          enabled: true
        },
        undefined
      );
      await CreateTask({
        task,
        result_config: {
          data_node_id: resultConfig.data_node_id.trim(),
          keep_duration: resultConfig.keep_duration.trim(),
          description: resultConfig.description.trim()
        }
      });
      Message.success(`采集任务“${task.task_name}”已创建`);
    } else {
      const input = currentTaskInput();
      const mutableTask = {
        task_id: editor.task_id,
        task_name: editor.task_name.trim(),
        description: editor.description.trim(),
        enabled: editor.enabled,
        collect_params: buildCollectionTaskParamsForUpdate(input)
      };
      await UpdateTask({
        space_id: editor.space_id,
        task_id: editor.task_id,
        task: mutableTask as Parameters<typeof UpdateTask>[0]["task"]
      });
      Message.success("更新成功");
    }
    editorVisible.value = false;
    await getTaskList();
    return true;
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "保存任务失败");
    return false;
  } finally {
    submitLoading.value = false;
  }
}

async function getTaskList() {
  const spaceId = selectedSpaceId.value;
  if (!spaceId) {
    taskList.value = [];
    pagination.total = 0;
    return;
  }
  loading.value = true;
  try {
    const response = await GetTaskList({
      space_id: spaceId,
      page: { page: pagination.current, size: pagination.pageSize },
      ...(filters.taskId.trim() ? { task_id: filters.taskId.trim() } : {}),
      ...(filters.dataType ? { data_type: filters.dataType } : {}),
      ...(selectEnabledFilter() === undefined ? {} : { enabled: selectEnabledFilter() })
    });
    taskList.value = (response.tasks || []).map(normalizeCollectionTask);
    pagination.total = Number(response.page?.total ?? taskList.value.length);
    const requestedTaskID = typeof route.query.taskId === "string" ? route.query.taskId : "";
    if (requestedTaskID) {
      const requestedTask = taskList.value.find(task => task.task_id === requestedTaskID);
      if (requestedTask) onViewDetails(requestedTask);
    }
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "获取任务列表失败");
  } finally {
    loading.value = false;
  }
}

async function getDataTypeConfigs() {
  try {
    const response = await GetDataTypeConfigs();
    dataTypeConfigs.value = response.configs || [];
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "获取数据类型失败");
  }
}

async function loadSources() {
  const sources: CollectionSourceOption[] = [];
  if (!selectedSpaceId.value) {
    sourceOptions.value = [];
    return;
  }
  loadingSources.value = true;
  try {
    for (let page = 1; ; page += 1) {
      const response = await listDatasets({
        space_id: selectedSpaceId.value,
        page: { page, size: 500 }
      });
      for (const source of response.datasets || []) {
        if (source.status !== "active") continue;
        sources.push({
          source_id: source.dataset_id,
          name: source.name,
          data_source_id: source.data_source_id,
          data_kind: source.data_kind,
          attributes: source.attributes,
          freqs: source.freqs,
          keep_duration: source.keep_duration
        });
      }
      if (!response.page_result?.has_more || (response.datasets || []).length === 0) break;
    }
    sourceOptions.value = sources;
  } catch (error) {
    sourceOptions.value = [];
    Message.error(error instanceof Error ? error.message : "获取标的来源失败");
  } finally {
    loadingSources.value = false;
  }
}

async function loadTags() {
  const spaceID = selectedSpaceId.value;
  const token = ++tagListRequestToken;
  if (!spaceID) {
    tags.value = [];
    return;
  }
  loadingTags.value = true;
  try {
    const allTags: Tag[] = [];
    for (let page = 1; ; page += 1) {
      const response = await listTags(spaceID, { page, size: 500 });
      if (token !== tagListRequestToken || spaceID !== selectedSpaceId.value) return;
      allTags.push(...(response.tags || []));
      if (!response.page_result?.has_more || !(response.tags || []).length) break;
    }
    if (token !== tagListRequestToken || spaceID !== selectedSpaceId.value) return;
    tags.value = allTags;
  } catch (error) {
    if (token === tagListRequestToken) {
      tags.value = [];
      Message.error(error instanceof Error ? error.message : "获取标签失败");
    }
  } finally {
    if (token === tagListRequestToken) loadingTags.value = false;
  }
}

async function loadSourceSubjectTags() {
  if (editing.value || editor.data_type !== "kline_resample" || !selectedSpaceId.value || !sourceIdValue.value) return;
  try {
    const dataset = await getDataset({ space_id: selectedSpaceId.value, dataset_id: sourceIdValue.value });
    subjectTagIds.value = (dataset.dataset?.subject_tags || []).slice();
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "获取源行情标签失败");
  }
}

async function loadTaskSubjectTags(record: CollectionTaskRecord) {
  if (record.tag_ids.length) {
    subjectTagIds.value = record.tag_ids.slice();
    return;
  }
  const viewId = record.result?.view_id;
  if (!viewId || !record.space_id) return;
  try {
    const view = await getView({ space_id: record.space_id, view_id: viewId });
    const datasetId = view.view?.dataset_id;
    if (!datasetId) return;
    const dataset = await getDataset({ space_id: record.space_id, dataset_id: datasetId });
    subjectTagIds.value = (dataset.dataset?.subject_tags || []).slice();
  } catch (error) {
    Message.warning(error instanceof Error ? error.message : "读取任务标签失败");
  }
}

function buildCollectionTaskParamsForUpdate(input: CollectionTaskInput) {
  return buildCollectionTaskParams(input);
}

async function loadDataNodes() {
  loadingDataNodes.value = true;
  try {
    const nodes: DataNode[] = [];
    for (let page = 1; ; page += 1) {
      const response = await listDataNodes({ status: "active", page: { page, size: 500 } });
      nodes.push(...(response.items || []).map(item => item.node).filter(node => node?.node_id));
      if (!response.page_result?.has_more || (response.items || []).length === 0) break;
    }
    dataNodes.value = nodes;
  } catch (error) {
    dataNodes.value = [];
    Message.warning(error instanceof Error ? error.message : "获取结果 DataNode 失败");
  } finally {
    loadingDataNodes.value = false;
  }
}

function search() {
  pagination.current = 1;
  void getTaskList();
}

function onPageChange(current: number) {
  pagination.current = current;
  void getTaskList();
}

function onPageSizeChange(pageSize: number) {
  pagination.pageSize = pageSize;
  pagination.current = 1;
  void getTaskList();
}

async function handleEnableChange(record: CollectionTaskRecord, enabled: boolean) {
  const spaceId = record.space_id || selectedSpaceId.value;
  if (!spaceId) {
    Message.error("请先选择空间");
    return;
  }
  try {
    if (!enabled) {
      await DisableTask({ space_id: spaceId, task_id: record.task_id });
    } else {
      await UpdateTask({
        space_id: spaceId,
        task_id: record.task_id,
        task: {
          task_id: record.task_id,
          task_name: record.task_name,
          description: record.description,
          enabled: true
        }
      });
    }
    Message.success("状态更新成功");
    await getTaskList();
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "状态更新失败");
  }
}

function onViewDetails(record: CollectionTaskRecord) {
  detailData.value = record;
  detailVisible.value = true;
}

function openDelete(record: CollectionTaskRecord) {
  deleteTarget.value = record;
  deleteResultData.value = false;
  deleteVisible.value = true;
}

async function handleDeleteOk(): Promise<boolean> {
  const record = deleteTarget.value;
  const spaceId = record?.space_id || selectedSpaceId.value;
  if (!record || !spaceId) {
    Message.error("请先选择空间");
    return false;
  }
  deleteLoading.value = true;
  try {
    await DeleteTask({
      space_id: spaceId,
      task_id: record.task_id,
      delete_result_data: deleteResultData.value
    });
    Message.success(deleteResultData.value ? "任务及结果数据已删除" : "任务已删除，结果数据已保留");
    deleteVisible.value = false;
    await getTaskList();
    return true;
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "删除任务失败");
    return false;
  } finally {
    deleteLoading.value = false;
  }
}

async function openBackfill(record: CollectionTaskRecord) {
  try {
    const input = parseCollectionTaskInput(record);
    if (input.dataType !== "kline_resample") throw new Error("当前任务不是 K 线重采样任务");
    const source = sourceOptions.value.find(item => item.source_id === input.sourceId);
    backfillTarget.value = {
      taskId: record.task_id,
      targetFrequency: input.frequency,
      sourceKeepDuration: source?.keep_duration || ""
    };
    activeBackfill.value = null;
    backfillVisible.value = true;
    activeBackfill.value = await getKlineResampleBackfillStatus(selectedSpaceId.value || record.space_id || "", record.task_id);
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "读取回填状态失败");
  }
}

async function onBackfillChanged() {
  await getTaskList();
  if (backfillTarget.value) {
    activeBackfill.value = await getKlineResampleBackfillStatus(selectedSpaceId.value || "", backfillTarget.value.taskId);
  }
}

watch(selectedSpaceId, () => {
  tagListRequestToken += 1;
  if (editorVisible.value && editor.space_id !== selectedSpaceId.value) {
    editorVisible.value = false;
    outputFieldsVisible.value = false;
    taskTypeFieldRequestToken += 1;
    Message.info("空间已切换，请重新打开采集任务窗口");
  }
  outputCatalogRequestToken += 1;
  outputCatalogSpaceID.value = "";
  outputFieldCatalog.value = [];
  outputFieldGroups.value = [];
  outputFieldLoadError.value = false;
  loadingOutputFields.value = false;
  if (!editing.value) selectedOutputFields.value = [];
  if (outputFieldsVisible.value) void loadOutputFieldCatalog();
  pagination.current = 1;
  void getTaskList();
  void loadSources();
  void loadTags();
});

watch(
  () => [editor.provider, editor.market_type, frequencyValue.value, sourceFrequencyValue.value],
  () => {
    if (sourceIdValue.value && !resampleSourceOptions.value.some(source => source.source_id === sourceIdValue.value)) {
      sourceIdValue.value = "";
    }
  }
);

onMounted(() => {
  void getTaskList();
  void getDataTypeConfigs();
  void loadSources();
  void loadTags();
  void loadDataNodes();
});
</script>

<style scoped>
.field-hint { display: block; margin-top: 4px; color: var(--color-text-3); font-size: 12px; }
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
.editor-section__header h3 { margin: 0; color: var(--color-text-1); font-size: 14px; font-weight: 600; }
.editor-section__header span { color: var(--color-text-3); font-size: 12px; }
.output-selection-summary { display: flex; align-items: center; gap: 12px; }
.output-selection-summary strong { color: var(--color-text-2); font-size: 13px; font-weight: 500; }
.selected-output-tags { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; margin-top: 12px; }
.output-field-error { margin-top: 12px; }
.output-field-picker-spin { display: block; margin-top: 14px; }
.output-field-toolbar { display: flex; align-items: center; gap: 14px; margin-bottom: 12px; }
.output-field-toolbar > :first-child { max-width: 420px; }
.output-field-toolbar > span { flex: none; color: var(--color-text-3); font-size: 12px; }
.output-field-sections { display: grid; gap: 10px; max-height: min(56vh, 560px); overflow: auto; padding: 1px 4px 4px 1px; }
.output-field-section { overflow: hidden; border: 1px solid var(--color-border-2); border-radius: 6px; background: var(--color-bg-2); }
.output-field-section__header { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 8px 12px; border-bottom: 1px solid var(--color-border-2); background: var(--color-fill-1); }
.output-field-section__header > div { display: flex; align-items: center; gap: 10px; min-width: 0; }
.output-field-section__header strong { overflow: hidden; color: var(--color-text-1); font-size: 13px; text-overflow: ellipsis; white-space: nowrap; }
.output-field-section__header span { flex: none; color: var(--color-text-3); font-size: 12px; }
.output-field-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 1px 12px; padding: 8px 12px; }
.output-field-option { display: flex; align-items: flex-start; gap: 8px; min-width: 0; padding: 6px 2px; cursor: pointer; }
.output-field-option__text { display: flex; flex-direction: column; min-width: 0; gap: 2px; }
.output-field-option__text strong { overflow: hidden; color: var(--color-text-1); font-size: 13px; font-weight: 500; text-overflow: ellipsis; white-space: nowrap; }
.output-field-option__text code { overflow: hidden; color: var(--color-text-3); font-size: 11px; text-overflow: ellipsis; white-space: nowrap; }
.output-field-option--unsupported { cursor: not-allowed; opacity: 0.55; }
.output-field-option__text small { color: var(--color-text-3); font-size: 11px; }
.output-field-option--unsupported { cursor: not-allowed; opacity: 0.55; }
.output-field-option__text small { color: var(--color-text-3); font-size: 11px; }
.task-toolbar {
  margin-bottom: var(--moox-space-toolbar-table);
}

.moox-inner {
  min-height: 100%;
}

pre {
  max-height: 220px;
  margin: 0;
  padding: var(--moox-space-2);
  overflow: auto;
  border-radius: 4px;
  background: var(--color-fill-1);
  font-family: monospace;
  font-size: 12px;
}

.immutable-hint {
  margin-bottom: var(--moox-space-4);
}

.delete-options {
  margin-top: var(--moox-space-4);
}

@media (max-width: 640px) {
  .editor-section { padding: 12px 12px 0; }
  .editor-section__header { align-items: flex-start; flex-direction: column; gap: 3px; margin-bottom: 12px; }
  .output-field-grid { grid-template-columns: minmax(0, 1fr); }
  .output-field-toolbar { align-items: stretch; flex-direction: column; gap: 6px; }
}
</style>
