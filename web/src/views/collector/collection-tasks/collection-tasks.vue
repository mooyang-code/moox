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
            <a-option v-for="config in dataTypeConfigs" :key="config.data_type" :value="config.data_type">
              {{ config.type_name }}
            </a-option>
          </a-select>
          <a-select v-model="filters.provider" placeholder="数据源" allow-clear style="width: 140px">
            <a-option v-for="source in providerOptions" :key="source.value" :value="source.value">
              {{ source.label }}
            </a-option>
          </a-select>
          <a-select v-model="filters.market" placeholder="市场" allow-clear style="width: 120px">
            <a-option value="spot">现货</a-option>
            <a-option value="swap">永续合约</a-option>
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
            <a-table-column title="数据源" data-index="provider" :width="120" />
            <a-table-column title="市场" data-index="market_type" :width="100" />
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
      width="760px"
      :ok-loading="submitLoading"
      @before-ok="handleOk"
      @close="afterClose"
      @cancel="afterClose"
    >
      <a-form ref="formRef" auto-label-width :rules="rules" :model="editor" layout="vertical">
        <a-alert v-if="editing" type="info" show-icon class="immutable-hint">
          数据类型、市场、结果身份和结果形态不可修改，如需变更请创建新任务。
        </a-alert>
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

        <a-row :gutter="16">
          <a-col :span="12">
            <a-form-item field="data_type" label="数据类型" required>
              <a-select v-model="editor.data_type" placeholder="请选择数据类型" :disabled="editing" @change="onDataTypeChange">
                <a-option v-for="config in dataTypeConfigs" :key="config.data_type" :value="config.data_type">
                  {{ config.type_name }}
                </a-option>
              </a-select>
            </a-form-item>
          </a-col>
          <a-col :span="12">
            <a-form-item field="provider" label="数据源" required>
              <a-select
                v-model="editor.provider"
                placeholder="请选择数据源"
                :loading="loadingProviders"
                :disabled="editing"
                allow-clear
              >
                <a-option v-for="source in editorProviderOptions" :key="source.value" :value="source.value">
                  {{ source.label }}
                </a-option>
              </a-select>
            </a-form-item>
          </a-col>
        </a-row>

        <a-row :gutter="16">
          <a-col :span="12">
            <a-form-item field="market_type" label="市场类型" required>
              <a-radio-group v-model="editor.market_type" type="button" :disabled="editing">
                <a-radio value="spot">现货</a-radio>
                <a-radio value="swap">永续合约</a-radio>
              </a-radio-group>
            </a-form-item>
          </a-col>
          <a-col :span="12">
            <a-form-item field="frequency" :label="editor.data_type === 'kline_resample' ? '目标频率' : '采集频率'" required>
              <a-input v-model="frequencyValue" placeholder="例如 1m、5m、1h" allow-clear :disabled="editing" />
            </a-form-item>
          </a-col>
        </a-row>

        <a-form-item v-if="editor.data_type === 'kline'" label="标的来源" required>
          <a-select
            v-model="symbolSourceId"
            placeholder="请选择标的来源"
            :loading="loadingSources"
            allow-search
            allow-clear
            :disabled="editing"
          >
            <a-option v-for="source in symbolSourceOptions" :key="source.source_id" :value="source.source_id">
              {{ source.name || "标的来源" }}
            </a-option>
          </a-select>
        </a-form-item>
        <a-form-item v-else-if="editor.data_type === 'instrument'" label="标的来源">
          <a-input model-value="交易所提供的全量标的" disabled />
        </a-form-item>

        <template v-if="editor.data_type === 'kline_resample'">
          <a-form-item label="源行情" required>
            <a-select
              v-model="sourceIdValue"
              placeholder="请选择源行情"
              :loading="loadingSources"
              allow-search
              allow-clear
              :disabled="editing"
            >
              <a-option v-for="source in resampleSourceOptions" :key="source.source_id" :value="source.source_id">
                {{ source.name || "源行情" }}
              </a-option>
            </a-select>
          </a-form-item>
          <a-row :gutter="16">
            <a-col :span="12">
              <a-form-item label="源周期" required>
                <a-input v-model="sourceFrequencyValue" placeholder="例如 1m、5m、1h" allow-clear :disabled="editing" />
              </a-form-item>
            </a-col>
            <a-col :span="12">
              <a-form-item label="序列标签" required>
                <a-input v-model="sourceSeriesTagValue" placeholder="例如 venue:binance" allow-clear :disabled="editing" />
              </a-form-item>
            </a-col>
          </a-row>
          <a-form-item label="收盘等待（毫秒）">
            <a-input-number
              v-model="settleDelayMSValue"
              :min="0"
              :max="86400000"
              :precision="0"
              placeholder="留空使用默认值"
              allow-clear
              :disabled="editing"
              style="width: 100%"
            />
          </a-form-item>
        </template>

        <a-form-item field="enabled" label="启用状态" required>
          <a-select v-model="editor.enabled">
            <a-option :value="true">启用</a-option>
            <a-option :value="false">禁用</a-option>
          </a-select>
        </a-form-item>

        <a-form-item v-if="editing" label="任务 ID">
          <a-input :model-value="editor.task_id" disabled />
        </a-form-item>
        <a-form-item v-if="editing" label="创建人">
          <a-input :model-value="editor.creator || '-'" disabled />
        </a-form-item>

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

    <a-modal v-model:visible="detailVisible" title="任务详情" :footer="false" width="800px">
      <a-descriptions v-if="detailData" :column="2" bordered>
        <a-descriptions-item label="任务 ID">{{ detailData.task_id }}</a-descriptions-item>
        <a-descriptions-item label="任务名称">{{ detailData.task_name || "-" }}</a-descriptions-item>
        <a-descriptions-item label="数据类型">{{ dataTypeLabel(detailData.data_type) }}</a-descriptions-item>
        <a-descriptions-item label="数据源">{{ detailData.provider || "-" }}</a-descriptions-item>
        <a-descriptions-item label="市场">{{ detailData.market_type || "-" }}</a-descriptions-item>
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
  GetTaskList,
  UpdateTask,
  getKlineResampleBackfillStatus,
  type DataTypeConfig
} from "@/api/collector";
import { listDataNodes, listDatasets } from "@/api/storage/metadata";
import type { DataNode } from "@/api/storage/types";
import { useSpaceStore } from "@/store/modules/space";
import { useUserInfoStore } from "@/store/modules/user-info";
import { storeToRefs } from "pinia";
import {
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
const loadingProviders = ref(false);
const loadingSources = ref(false);
const loadingDataNodes = ref(false);
const taskList = ref<CollectionTaskRecord[]>([]);
const dataTypeConfigs = ref<DataTypeConfig[]>([]);
const sourceOptions = ref<CollectionSourceOption[]>([]);
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
const symbolSourceId = ref("");
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
  provider: string;
  market: string;
  enabled: EnabledFilter;
}>({
  taskId: "",
  dataType: "",
  provider: "",
  market: "",
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
const selectedDataTypeConfig = computed(() => dataTypeConfigs.value.find(item => item.data_type === editor.data_type));
const providerOptions = computed(() => {
  const options = filters.dataType
    ? dataTypeConfigs.value.find(item => item.data_type === filters.dataType)?.data_source_options
    : undefined;
  return dataSourceOptionsFromConfig(options);
});
const editorProviderOptions = computed(() => dataSourceOptionsFromConfig(selectedDataTypeConfig.value?.data_source_options));
const symbolSourceOptions = computed(() =>
  sourceOptions.value.filter(source => collectionSourceMatches(source, editor.provider, "instrument", editor.market_type))
);
const resampleSourceOptions = computed(() =>
  sourceOptions.value.filter(source =>
    collectionSourceMatches(source, editor.provider, "kline_resample", editor.market_type, sourceFrequencyValue.value)
  )
);

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
  provider: [{ required: true, message: "请选择 Provider" }],
  market_type: [{ required: true, message: "请选择市场" }],
  enabled: [{ required: true, message: "请选择启用状态" }]
};

function dataTypeOptions(value: unknown): Array<{ label?: string; value?: string }> {
  if (!value) return [];
  if (Array.isArray(value)) {
    return value.map(item => (typeof item === "string" ? { value: item } : (item as { label?: string; value?: string })));
  }
  if (typeof value !== "object") return [];
  const options = (value as { options?: unknown }).options;
  return Array.isArray(options)
    ? options.map(item => (typeof item === "string" ? { value: item } : (item as { label?: string; value?: string })))
    : [];
}

function dataSourceOptionsFromConfig(value: unknown): Array<{ label: string; value: string }> {
  return dataTypeOptions(value)
    .filter(option => option.value)
    .map(option => ({
      value: option.value as string,
      label: option.label || providerLabel(option.value as string)
    }));
}

function providerLabel(value: string) {
  const labels: Record<string, string> = {
    binance: "币安（Binance）",
    moox: "MooX",
    okx: "OKX",
    stockcn_multi: "A 股行情"
  };
  return labels[value] || value;
}

function dataTypeLabel(value?: string) {
  return dataTypeConfigs.value.find(config => config.data_type === value)?.type_name || value || "-";
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
  symbolSourceId.value = "";
  sourceIdValue.value = "";
  sourceFrequencyValue.value = "";
  sourceSeriesTagValue.value = "";
  settleDelayMSValue.value = undefined;
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
  symbolSourceId.value = input.symbolSourceId || "";
  sourceIdValue.value = input.sourceId || "";
  sourceFrequencyValue.value = input.sourceFrequency || "";
  sourceSeriesTagValue.value = input.sourceSeriesTag || "";
  settleDelayMSValue.value = input.settleDelayMS;
  editorVisible.value = true;
}

function onDataTypeChange() {
  if (editing.value) return;
  editor.provider = "";
  frequencyValue.value = "";
  symbolSourceId.value = "";
  sourceIdValue.value = "";
  sourceFrequencyValue.value = "";
  sourceSeriesTagValue.value = "";
  settleDelayMSValue.value = undefined;
}

function afterClose() {
  formRef.value?.clearValidate?.();
  editorVisible.value = false;
}

function currentTaskInput(): CollectionTaskInput {
  if (!editor.data_type) throw new Error("请选择数据类型");
  return {
    dataType: editor.data_type,
    provider: editor.provider,
    market: editor.market_type,
    frequency: frequencyValue.value,
    symbolSource: editor.data_type === "instrument" ? "exchange" : "dataset",
    symbolSourceId: symbolSourceId.value,
    sourceId: sourceIdValue.value,
    sourceFrequency: sourceFrequencyValue.value,
    sourceSeriesTag: sourceSeriesTagValue.value,
    settleDelayMS: settleDelayMSValue.value
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
    if (input.dataType === "kline" && !symbolSourceId.value.trim()) {
      throw new Error("请选择标的来源");
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
      const task = buildCollectionTaskPayload(
        input,
        {
          task_name: editor.task_name,
          description: editor.description,
          space_id: editor.space_id,
          creator: editor.creator || account.value?.user?.userName || "",
          enabled: editor.enabled
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
      await UpdateTask({
        space_id: editor.space_id,
        task_id: editor.task_id,
        task: {
          task_id: editor.task_id,
          task_name: editor.task_name.trim(),
          description: editor.description.trim(),
          enabled: editor.enabled
        }
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
      ...(filters.provider ? { provider: filters.provider } : {}),
      ...(filters.market ? { market_type: filters.market } : {}),
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
  pagination.current = 1;
  void getTaskList();
  void loadSources();
});

watch(
  () => [editor.provider, editor.market_type, frequencyValue.value, sourceFrequencyValue.value],
  () => {
    if (symbolSourceId.value && !symbolSourceOptions.value.some(source => source.source_id === symbolSourceId.value)) {
      symbolSourceId.value = "";
    }
    if (sourceIdValue.value && !resampleSourceOptions.value.some(source => source.source_id === sourceIdValue.value)) {
      sourceIdValue.value = "";
    }
  }
);

onMounted(() => {
  void getTaskList();
  void getDataTypeConfigs();
  void loadSources();
  void loadDataNodes();
});
</script>

<style scoped>
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
</style>
