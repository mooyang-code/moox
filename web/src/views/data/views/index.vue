<template>
  <div class="moox-page">
    <div class="moox-inner">
    <div class="page-head">
      <div class="page-head__title">
        <slot name="page-title">
          <h2>{{ props.pageTitle }}</h2>
        </slot>
      </div>
      <a-space>
        <a-button type="primary" :disabled="!selectedSpaceId" @click="openCreate">
          <template #icon><icon-plus /></template>
          新增视图
        </a-button>
        <a-button :disabled="!selectedSpaceId" @click="load">
          <template #icon><icon-refresh /></template>
          刷新
        </a-button>
      </a-space>
    </div>

    <a-alert v-if="!selectedSpaceId" type="warning" show-icon>请先在顶部选择空间</a-alert>

    <a-table
      v-else
      row-key="view_id"
      size="small"
      :bordered="{ cell: true }"
      :loading="loading"
      :data="visibleRows"
      :pagination="pagination"
      :scroll="{ x: 'max-content' }"
      @page-change="onPageChange"
      @page-size-change="onPageSizeChange"
    >
      <template #columns>
        <a-table-column title="视图ID" data-index="view_id" :width="170" />
        <a-table-column title="中文名" data-index="name" :width="160" />
        <a-table-column title="引擎" data-index="engine" :width="100" />
        <a-table-column title="主数据集" data-index="primary_dataset_id" :width="150" />
        <a-table-column title="频率" :width="90">
          <template #cell="{ record }">{{ viewFreqLabel(record) }}</template>
        </a-table-column>
        <a-table-column title="版本" :width="90">
          <template #cell="{ record }">{{ record.view_version || 0 }}</template>
        </a-table-column>
        <a-table-column title="活跃版本" :width="100">
          <template #cell="{ record }">{{ record.active_view_version || 0 }}</template>
        </a-table-column>
        <a-table-column title="切换状态" :width="120">
          <template #cell="{ record }">
            <a-tag size="small" :color="viewIndexStateColor(record)">{{ viewIndexStateLabel(record) }}</a-tag>
          </template>
        </a-table-column>
        <a-table-column title="更新时间" :width="180">
          <template #cell="{ record }">{{ formatTime(record.updated_at) }}</template>
        </a-table-column>
        <a-table-column title="操作" :width="250" align="center" :fixed="'right'">
          <template #cell="{ record }">
            <a-space>
              <a-button size="mini" type="text" @click="openColumns(record)">列</a-button>
              <a-button size="mini" type="text" @click="openEdit(record)">编辑</a-button>
            </a-space>
          </template>
        </a-table-column>
      </template>
    </a-table>

    </div>

    <a-modal v-model:visible="visible" width="820px" :title="modalTitle" @ok="submit">
      <a-form :model="form" auto-label-width>
        <a-form-item field="view_id" label="视图ID" required>
          <a-input v-model="form.view_id" :disabled="editing" placeholder="例如 kline_view" />
        </a-form-item>
        <a-form-item field="name" label="中文名" required>
          <a-input v-model="form.name" :max-length="10" show-word-limit placeholder="例如 K线视图" />
        </a-form-item>
        <a-form-item field="description" label="描述">
          <a-textarea v-model="form.description" :auto-size="{ minRows: 3, maxRows: 5 }" />
        </a-form-item>
        <a-form-item field="primary_dataset_id" label="主数据集" required>
          <a-select v-model="form.primary_dataset_id" allow-search placeholder="选择主数据集">
            <a-option v-for="item in selectableDatasets" :key="item.dataset_id" :value="item.dataset_id">
              {{ item.name }} ({{ item.dataset_id }})
            </a-option>
          </a-select>
        </a-form-item>
        <a-form-item v-if="isTimeSeriesPrimaryDataset" field="view_freq" label="频率" required>
          <a-select v-model="form.view_freq" allow-search placeholder="请选择频率">
            <a-option v-for="freq in primaryFreqOptions" :key="freq" :value="freq">{{ freq }}</a-option>
          </a-select>
        </a-form-item>
        <a-form-item v-else field="record_view_mode" label="Record模式">
          <a-select v-model="form.record_view_mode">
            <a-option value="current">CURRENT：当前快照</a-option>
            <a-option value="history">HISTORY：历史版本</a-option>
          </a-select>
        </a-form-item>
        <a-form-item field="dataset_ids" label="包含数据集">
          <a-select v-model="form.dataset_ids" multiple allow-search placeholder="选择视图包含的数据集">
            <a-option v-for="item in includedDatasetOptions" :key="item.dataset_id" :value="item.dataset_id">
              {{ item.name }} ({{ item.dataset_id }})
            </a-option>
          </a-select>
        </a-form-item>
        <a-form-item v-if="!editing && form.primary_dataset_id" label="视图列">
          <div class="draft-columns">
            <div class="draft-columns-head">
              <span>已根据所选数据集自动生成，可删除不需要的列</span>
              <a-button size="mini" :loading="columnsLoading" @click="refreshDraftColumns">
                <template #icon><icon-refresh /></template>
                重新生成
              </a-button>
            </div>
            <a-table
              row-key="column_name"
              size="small"
              :bordered="{ cell: true }"
              :pagination="false"
              :loading="columnsLoading"
              :data="draftColumns"
              :scroll="{ x: 'max-content', y: 260 }"
            >
              <template #columns>
                <a-table-column title="中文名" :width="120">
                  <template #cell="{ record }">{{ record.attributes?.display_name || '-' }}</template>
                </a-table-column>
                <a-table-column title="技术列名" data-index="column_name" :width="170" />
                <a-table-column title="来源数据集" :width="180">
                  <template #cell="{ record }">{{ draftColumnDatasetName(record) }}</template>
                </a-table-column>
                <a-table-column title="来源字段" :width="160">
                  <template #cell="{ record }">{{ draftColumnSourceName(record) }}</template>
                </a-table-column>
                <a-table-column title="值类型" :width="110">
                  <template #cell="{ record }">{{ optionLabel(fieldValueTypeOptions, record.value_type) }}</template>
                </a-table-column>
                <a-table-column title="操作" :width="90" align="center" :fixed="'right'">
                  <template #cell="{ record }">
                    <a-button size="mini" type="text" status="danger" @click="removeDraftColumn(record.column_name)">
                      删除
                    </a-button>
                  </template>
                </a-table-column>
              </template>
            </a-table>
          </div>
        </a-form-item>
        <a-form-item field="retention_window" label="索引保留窗口">
          <a-input v-model="form.retention_window" placeholder="例如 90d，可留空" />
        </a-form-item>
        <a-form-item field="filter_json" label="过滤JSON">
          <a-textarea v-model="form.filter_json" :auto-size="{ minRows: 4, maxRows: 8 }" />
        </a-form-item>
        <a-form-item field="status" label="状态">
          <a-select v-model="form.status">
            <a-option v-for="item in statusOptions" :key="item.value" :value="item.value">{{ item.label }}</a-option>
          </a-select>
        </a-form-item>
      </a-form>
    </a-modal>

    <a-drawer v-model:visible="columnsVisible" width="900px" :footer="false">
      <template #title>视图结果列：{{ activeView?.view_id }}</template>
      <ViewColumnPanel :space-id="selectedSpaceId" :view-id="activeView?.view_id || ''" />
    </a-drawer>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { createView, listDatasetColumns, listDatasets, listViews, updateView, upsertViewColumn } from "@/api/storage/metadata";
import type { Dataset, DatasetColumn, View, ViewColumn } from "@/api/storage/types";
import { useSpaceStore } from "@/store/modules/space";
import ViewColumnPanel from "./components/view-column-panel.vue";
import {
  applyPageResult,
  defaultPagination,
  fieldValueTypeOptions,
  formatTime,
  isTimeSeriesDataKind,
  jsonText,
  optionLabel,
  statusOptions,
  validateChineseDisplayName,
  validateLowerSnakeId
} from "@/views/data/shared/metadata-utils";
import {
  mergeViewAttribution,
  datasetMatchesAttribution,
  isLikelyFactorResultDataset,
  isLikelyFactorResultDatasetId,
  viewMatchesAttribution,
  type DatasetRole,
  type OwnerModule,
  type ViewRole
} from "@/views/data/shared/module-attribution";
import {
  buildDraftViewColumns,
  buildTimeSeriesViewFilterJSON,
  buildViewDatasetIds,
  availableIncludedDatasets,
  defaultViewEngine,
  defaultViewGrainKeys,
  freqFromViewFilterJSON,
  freqOptionsForPrimaryDataset,
  removePrimaryFromIncludes
} from "./view-form-utils";

defineOptions({ name: "DataViews" });

const props = withDefaults(defineProps<{
  pageTitle?: string;
  ownerModule?: OwnerModule;
  viewRole?: ViewRole;
  filterOwnerModules?: OwnerModule[];
  filterDatasetRoles?: DatasetRole[];
  filterViewRoles?: ViewRole[];
  includeUnowned?: boolean;
  managedBy?: string;
  allowedPrimaryDatasetIds?: string[];
  excludedPrimaryDatasetIds?: string[];
  excludeLikelyFactorDatasets?: boolean;
}>(), {
  pageTitle: "视图列表",
  ownerModule: undefined,
  viewRole: undefined,
  filterOwnerModules: undefined,
  filterDatasetRoles: undefined,
  filterViewRoles: undefined,
  includeUnowned: false,
  managedBy: undefined,
  allowedPrimaryDatasetIds: undefined,
  excludedPrimaryDatasetIds: undefined,
  excludeLikelyFactorDatasets: false,
});

type ViewForm = View & { view_freq?: string };

const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);
const rows = ref<View[]>([]);
const allowedPrimaryDatasetIdSet = computed(() => new Set((props.allowedPrimaryDatasetIds || []).filter(Boolean)));
const excludedPrimaryDatasetIdSet = computed(() => new Set((props.excludedPrimaryDatasetIds || []).filter(Boolean)));
const hasViewAttributionFilter = computed(() =>
  Boolean(props.filterOwnerModules?.length || props.filterViewRoles?.length || props.includeUnowned),
);
const hasDatasetAttributionFilter = computed(() =>
  Boolean(props.filterOwnerModules?.length || props.filterDatasetRoles?.length || props.includeUnowned),
);
const datasetById = computed(() => new Map(datasets.value.map((item) => [item.dataset_id, item])));
const visibleRows = computed(() =>
  rows.value.filter((item) => {
    const matchedByDataset =
      allowedPrimaryDatasetIdSet.value.size > 0 && allowedPrimaryDatasetIdSet.value.has(item.primary_dataset_id);
    if (!matchedByDataset && excludedPrimaryDatasetIdSet.value.has(item.primary_dataset_id)) {
      return false;
    }
    if (props.excludeLikelyFactorDatasets && !matchedByDataset && viewUsesLikelyFactorDataset(item)) {
      return false;
    }
    const matchedByAttrs = viewMatchesAttribution(item, {
      ownerModules: props.filterOwnerModules,
      viewRoles: props.filterViewRoles,
      includeUnowned: props.includeUnowned,
    });
    if (allowedPrimaryDatasetIdSet.value.size > 0 && hasViewAttributionFilter.value) {
      return matchedByDataset || matchedByAttrs;
    }
    if (allowedPrimaryDatasetIdSet.value.size > 0) {
      return matchedByDataset;
    }
    return matchedByAttrs;
  }),
);
const datasets = ref<Dataset[]>([]);
const selectableDatasets = computed(() =>
  datasets.value.filter((item) => {
    const matchedByAllowedId =
      allowedPrimaryDatasetIdSet.value.size > 0 && allowedPrimaryDatasetIdSet.value.has(item.dataset_id);
    if (!matchedByAllowedId && excludedPrimaryDatasetIdSet.value.has(item.dataset_id)) {
      return false;
    }
    if (props.excludeLikelyFactorDatasets && !matchedByAllowedId && isLikelyFactorResultDataset(item)) {
      return false;
    }
    const matchedByAttrs = datasetMatchesAttribution(item, {
      ownerModules: props.filterOwnerModules,
      datasetRoles: props.filterDatasetRoles,
      includeUnowned: props.includeUnowned,
    });
    if (allowedPrimaryDatasetIdSet.value.size > 0 && hasDatasetAttributionFilter.value) {
      return matchedByAllowedId || matchedByAttrs;
    }
    if (allowedPrimaryDatasetIdSet.value.size > 0) {
      return matchedByAllowedId;
    }
    return matchedByAttrs;
  }),
);
const hasAttributionFilter = computed(() =>
  Boolean(
    props.filterOwnerModules?.length ||
      props.filterViewRoles?.length ||
      props.filterDatasetRoles?.length ||
      props.includeUnowned ||
      props.allowedPrimaryDatasetIds?.length ||
      props.excludedPrimaryDatasetIds?.length ||
      props.excludeLikelyFactorDatasets,
  ),
);
const draftColumns = ref<ViewColumn[]>([]);
const loading = ref(false);
const columnsLoading = ref(false);
const visible = ref(false);
const editing = ref(false);
const columnsVisible = ref(false);
const activeView = ref<View>();
const pagination = reactive(defaultPagination());
let draftLoadSeq = 0;

const form = reactive<ViewForm>({
  space_id: "",
  view_id: "",
  name: "",
  description: "",
  primary_dataset_id: "",
  dataset_ids: [],
  grain_keys: [],
  filter_json: "{}",
  view_freq: "",
  engine: "",
  retention_window: "",
  record_view_mode: "current",
  status: "active",
  attributes: {}
});

const modalTitle = computed(() => (editing.value ? "编辑视图" : "新增视图"));
const primaryDataset = computed(() => datasets.value.find(item => item.dataset_id === form.primary_dataset_id));
const primaryFreqOptions = computed(() => freqOptionsForPrimaryDataset(datasets.value, form.primary_dataset_id));
const isTimeSeriesPrimaryDataset = computed(() => isTimeSeriesDataKind(primaryDataset.value?.data_kind));
const includedDatasetOptions = computed(() =>
  availableIncludedDatasets(selectableDatasets.value, form.primary_dataset_id, form.view_freq || "")
);

async function loadDatasets() {
  if (!selectedSpaceId.value) {
    datasets.value = [];
    return;
  }
  datasets.value = await listAllDatasets(selectedSpaceId.value);
}

async function load() {
  if (!selectedSpaceId.value) {
    rows.value = [];
    return;
  }
  loading.value = true;
  try {
    await loadDatasets();
    if (hasAttributionFilter.value) {
      rows.value = await listAllViews(selectedSpaceId.value);
      pagination.total = visibleRows.value.length;
      return;
    }
    const rsp = await listViews({
      space_id: selectedSpaceId.value,
      page: { page: pagination.current, size: pagination.pageSize }
    });
    rows.value = rsp.views || [];
    applyPageResult(pagination, rsp.page_result);
  } finally {
    loading.value = false;
  }
}

async function listAllDatasets(spaceId: string) {
  const datasets: Dataset[] = [];
  const size = 500;
  for (let pageNo = 1; ; pageNo += 1) {
    const rsp = await listDatasets({ space_id: spaceId, page: { page: pageNo, size } });
    datasets.push(...(rsp.datasets || []));
    if (!rsp.page_result?.has_more || (rsp.datasets || []).length === 0) {
      return datasets;
    }
  }
}

async function listAllViews(spaceId: string) {
  const views: View[] = [];
  const size = 500;
  for (let pageNo = 1; ; pageNo += 1) {
    const rsp = await listViews({ space_id: spaceId, page: { page: pageNo, size } });
    views.push(...(rsp.views || []));
    if (!rsp.page_result?.has_more || (rsp.views || []).length === 0) {
      return views;
    }
  }
}

function viewUsesLikelyFactorDataset(view: View) {
  const dataset = datasetById.value.get(view.primary_dataset_id);
  if (dataset) {
    return isLikelyFactorResultDataset(dataset);
  }
  return isLikelyFactorResultDatasetId(view.primary_dataset_id);
}

function resetForm() {
  Object.assign(form, {
    space_id: selectedSpaceId.value,
    view_id: "",
    name: "",
    description: "",
    primary_dataset_id: "",
    dataset_ids: [],
    grain_keys: [],
    filter_json: "{}",
    view_freq: "",
    engine: "",
    retention_window: "",
    record_view_mode: "current",
    status: "active",
    attributes: {}
  });
  draftColumns.value = [];
}

function openCreate() {
  editing.value = false;
  resetForm();
  visible.value = true;
}

function openEdit(record: View) {
  editing.value = true;
  Object.assign(form, {
    ...record,
    dataset_ids: (record.dataset_ids || []).filter(datasetId => datasetId !== record.primary_dataset_id),
    grain_keys: record.grain_keys || [],
    filter_json: jsonText(record.filter_json),
    view_freq: freqFromViewFilterJSON(record.filter_json),
    record_view_mode: record.record_view_mode || "current",
  });
  syncViewFreq();
  draftColumns.value = [];
  visible.value = true;
}

function openColumns(record: View) {
  activeView.value = record;
  columnsVisible.value = true;
}

async function submit() {
  const spaceId = spaceStore.requireSpaceId();
  if (!form.view_id || !form.name || !form.primary_dataset_id) {
    Message.warning("请补全视图ID、中文名和主数据集");
    return;
  }
  const nameError = validateChineseDisplayName(form.name);
  if (nameError) {
    Message.warning(nameError);
    return;
  }
  const idError = validateLowerSnakeId(form.view_id, 30);
  if (idError) {
    Message.warning(`视图${idError}`);
    return;
  }
  let filterJSON = jsonText(form.filter_json);
  if (isTimeSeriesPrimaryDataset.value) {
    if (!form.view_freq) {
      Message.warning("请选择频率");
      return;
    }
    try {
      filterJSON = buildTimeSeriesViewFilterJSON(form.filter_json, form.view_freq);
    } catch {
      Message.warning("过滤JSON格式错误");
      return;
    }
  }
  const datasetIds = buildViewDatasetIds(form.primary_dataset_id, form.dataset_ids || []);
  const payload: View = {
    space_id: spaceId,
    view_id: form.view_id,
    name: form.name,
    description: form.description,
    primary_dataset_id: form.primary_dataset_id,
    dataset_ids: datasetIds,
    grain_keys: defaultViewGrainKeys(datasets.value, form.primary_dataset_id),
    filter_json: filterJSON,
    engine: defaultViewEngine(datasets.value, form.primary_dataset_id),
    retention_window: form.retention_window,
    record_view_mode: isTimeSeriesPrimaryDataset.value ? "RECORD_VIEW_MODE_UNSPECIFIED" : (form.record_view_mode === "history" ? "RECORD_VIEW_MODE_HISTORY" : "RECORD_VIEW_MODE_CURRENT"),
    status: form.status,
    attributes: mergeViewAttribution(form.attributes, {
      ownerModule: props.ownerModule,
      viewRole: props.viewRole,
      managedBy: props.managedBy,
      primaryDatasetRole: primaryDataset.value?.attributes?.dataset_role as DatasetRole | undefined,
    })
  };
  if (editing.value) {
    await updateView(payload);
  } else {
    await createView(payload);
    await saveDraftColumns(spaceId, form.view_id);
  }
  Message.success("视图已保存");
  visible.value = false;
  await load();
}

async function saveDraftColumns(spaceId: string, viewId: string) {
  for (const column of draftColumns.value) {
    await upsertViewColumn({
      ...column,
      space_id: spaceId,
      view_id: viewId
    });
  }
}

async function refreshDraftColumns() {
  const spaceId = selectedSpaceId.value;
  if (editing.value || !spaceId || !form.primary_dataset_id) {
    draftColumns.value = [];
    return;
  }
  const seq = ++draftLoadSeq;
  columnsLoading.value = true;
  try {
    const datasetIds = buildViewDatasetIds(form.primary_dataset_id, form.dataset_ids || []);
    const entries = await Promise.all(
      datasetIds.map(async datasetId => {
        const rsp = await listDatasetColumns({
          space_id: spaceId,
          dataset_id: datasetId,
          page: { page: 1, size: 500 }
        });
        return [datasetId, rsp.columns || []] as const;
      })
    );
    if (seq !== draftLoadSeq) return;
    const columnsByDataset = entries.reduce<Record<string, DatasetColumn[]>>((acc, [datasetId, columns]) => {
      acc[datasetId] = columns;
      return acc;
    }, {});
    draftColumns.value = buildDraftViewColumns(form.primary_dataset_id, form.dataset_ids || [], columnsByDataset);
  } finally {
    if (seq === draftLoadSeq) columnsLoading.value = false;
  }
}

function removeDraftColumn(columnName: string) {
  draftColumns.value = draftColumns.value.filter(item => item.column_name !== columnName);
}

function draftColumnDatasetName(record: ViewColumn) {
  const datasetId = record.origin_id?.split(".")[0] || "";
  const dataset = datasets.value.find(item => item.dataset_id === datasetId);
  return dataset ? `${dataset.name || dataset.dataset_id} (${dataset.dataset_id})` : datasetId || "-";
}

function draftColumnSourceName(record: ViewColumn) {
  return record.origin_id?.split(".").slice(1).join(".") || record.origin_id || "-";
}

function viewFreqLabel(record: View) {
  return freqFromViewFilterJSON(record.filter_json) || "-";
}

const viewIndexStateLabels: Record<string, string> = {
  "1": "准备中",
  "2": "构建中",
  "3": "追平中",
  "4": "待激活",
  "5": "失败",
  PREPARING: "准备中",
  BUILDING: "构建中",
  CATCHING_UP: "追平中",
  READY: "待激活",
  FAILED: "失败",
};

function normalizedViewIndexState(record: View) {
  return String(record.index_build?.state ?? "").replace(/^VIEW_INDEX_BUILD_STATE_/, "").toUpperCase();
}

function viewIndexStateLabel(record: View) {
  const state = normalizedViewIndexState(record);
  if (state) return viewIndexStateLabels[state] || state;
  return record.active_index_id ? "已激活" : "待构建";
}

function viewIndexStateColor(record: View) {
  const state = normalizedViewIndexState(record);
  if (state === "5" || state === "FAILED") return "red";
  if (state === "1" || state === "PREPARING") return "orange";
  if (state === "2" || state === "BUILDING" || state === "3" || state === "CATCHING_UP") return "arcoblue";
  return record.active_index_id ? "green" : "orange";
}

function onPageChange(page: number) {
  pagination.current = page;
  load();
}

function onPageSizeChange(pageSize: number) {
  pagination.current = 1;
  pagination.pageSize = pageSize;
  load();
}

function syncIncludedDatasets() {
  const next = removePrimaryFromIncludes(form.primary_dataset_id, form.dataset_ids || []);
  const allowed = new Set(includedDatasetOptions.value.map(item => item.dataset_id));
  const filtered = next.filter(datasetId => allowed.has(datasetId));
  if (filtered.join("|") !== (form.dataset_ids || []).join("|")) {
    form.dataset_ids = filtered;
    Message.warning("包含数据集已自动调整");
  }
}

function syncViewFreq() {
  if (!primaryFreqOptions.value.length) {
    form.view_freq = "";
    return;
  }
  if (!form.view_freq || !primaryFreqOptions.value.includes(form.view_freq)) {
    form.view_freq = primaryFreqOptions.value[0];
  }
}

watch(
  () => form.primary_dataset_id,
  () => {
    syncViewFreq();
    syncIncludedDatasets();
    refreshDraftColumns();
  }
);

watch(
  () => form.view_freq,
  () => {
    syncIncludedDatasets();
    refreshDraftColumns();
  }
);

watch(
  () => form.dataset_ids,
  () => {
    syncIncludedDatasets();
    refreshDraftColumns();
  },
  { deep: true }
);

watch(selectedSpaceId, () => {
  pagination.current = 1;
  load();
});
onMounted(load);
</script>

<style scoped>
.page-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 16px;
}

.page-head__title {
  display: flex;
  align-items: center;
  min-width: 0;
}

.page-head h2 {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
}

.draft-columns {
  width: 100%;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.draft-columns-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  color: var(--color-text-2);
  font-size: 13px;
}
</style>
