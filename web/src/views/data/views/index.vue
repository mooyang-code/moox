<template>
  <div class="metadata-page">
    <div class="page-head">
      <h2>视图列表</h2>
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
      :data="rows"
      :pagination="pagination"
      :scroll="{ x: 'max-content' }"
      @page-change="onPageChange"
      @page-size-change="onPageSizeChange"
    >
      <template #columns>
        <a-table-column title="视图ID" data-index="view_id" :width="170" />
        <a-table-column title="名称" data-index="name" :width="160" />
        <a-table-column title="引擎" data-index="engine" :width="100" />
        <a-table-column title="主数据集" data-index="primary_dataset_id" :width="150" />
        <a-table-column title="版本" :width="90">
          <template #cell="{ record }">{{ record.view_version || 0 }}</template>
        </a-table-column>
        <a-table-column title="活跃版本" :width="100">
          <template #cell="{ record }">{{ record.active_view_version || 0 }}</template>
        </a-table-column>
        <a-table-column title="构建状态" :width="110">
          <template #cell="{ record }">
            <a-tag size="small" :color="statusColor(record.build_status)">{{ record.build_status || "-" }}</a-tag>
          </template>
        </a-table-column>
        <a-table-column title="更新时间" :width="180">
          <template #cell="{ record }">{{ formatTime(record.updated_at) }}</template>
        </a-table-column>
        <a-table-column title="操作" :width="250" align="center" :fixed="'right'">
          <template #cell="{ record }">
            <a-space>
              <a-button size="mini" type="text" @click="openColumns(record)">列</a-button>
              <a-button size="mini" type="text" @click="rebuild(record)">重建</a-button>
              <a-button size="mini" type="text" @click="openEdit(record)">编辑</a-button>
            </a-space>
          </template>
        </a-table-column>
      </template>
    </a-table>

    <a-modal v-model:visible="visible" width="820px" :title="modalTitle" @ok="submit">
      <a-form :model="form" auto-label-width>
        <a-form-item field="view_id" label="视图ID" required>
          <a-input v-model="form.view_id" :disabled="editing" placeholder="例如 kline_view" />
        </a-form-item>
        <a-form-item field="name" label="名称" required>
          <a-input v-model="form.name" />
        </a-form-item>
        <a-form-item field="description" label="描述">
          <a-textarea v-model="form.description" :auto-size="{ minRows: 3, maxRows: 5 }" />
        </a-form-item>
        <a-form-item field="primary_dataset_id" label="主数据集" required>
          <a-select v-model="form.primary_dataset_id" allow-search placeholder="选择主数据集">
            <a-option v-for="item in datasets" :key="item.dataset_id" :value="item.dataset_id">
              {{ item.name }} ({{ item.dataset_id }})
            </a-option>
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
              size="mini"
              :bordered="{ cell: true }"
              :pagination="false"
              :loading="columnsLoading"
              :data="draftColumns"
              :scroll="{ x: 'max-content', y: 260 }"
            >
              <template #columns>
                <a-table-column title="列名" data-index="column_name" :width="170" />
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
        <a-form-item field="query_window" label="查询窗口">
          <a-input v-model="form.query_window" placeholder="例如 90d，可留空" />
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
import { rebuildRecordView, rebuildTimeSeriesView } from "@/api/storage/view";
import type { Dataset, DatasetColumn, View, ViewColumn } from "@/api/storage/types";
import { useSpaceStore } from "@/store/modules/space";
import ViewColumnPanel from "./components/view-column-panel.vue";
import {
  applyPageResult,
  defaultPagination,
  fieldValueTypeOptions,
  formatTime,
  jsonText,
  optionLabel,
  resolveViewRebuildKind,
  statusColor,
  statusOptions,
  validateLowerSnakeId
} from "@/views/data/shared/metadata-utils";
import {
  buildDraftViewColumns,
  buildViewDatasetIds,
  availableIncludedDatasets,
  defaultViewEngine,
  defaultViewGrainKeys,
  removePrimaryFromIncludes
} from "./view-form-utils";

defineOptions({ name: "DataViews" });

type ViewForm = View;

const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);
const rows = ref<View[]>([]);
const datasets = ref<Dataset[]>([]);
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
  engine: "",
  query_window: "",
  status: "active"
});

const modalTitle = computed(() => (editing.value ? "编辑视图" : "新增视图"));
const includedDatasetOptions = computed(() => availableIncludedDatasets(datasets.value));

async function loadDatasets() {
  if (!selectedSpaceId.value) {
    datasets.value = [];
    return;
  }
  const rsp = await listDatasets({ space_id: selectedSpaceId.value, page: { page: 1, size: 500 } });
  datasets.value = rsp.datasets || [];
}

async function load() {
  if (!selectedSpaceId.value) {
    rows.value = [];
    return;
  }
  loading.value = true;
  try {
    await loadDatasets();
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
    engine: "",
    query_window: "",
    status: "active"
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
    filter_json: jsonText(record.filter_json)
  });
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
    Message.warning("请补全视图ID、名称和主数据集");
    return;
  }
  const idError = validateLowerSnakeId(form.view_id, 30);
  if (idError) {
    Message.warning(`视图${idError}`);
    return;
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
    filter_json: jsonText(form.filter_json),
    engine: defaultViewEngine(datasets.value, form.primary_dataset_id),
    query_window: form.query_window,
    status: form.status
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

async function rebuild(record: View) {
  const spaceId = spaceStore.requireSpaceId();
  const rebuildKind = resolveViewRebuildKind(datasets.value, record.primary_dataset_id);
  if (rebuildKind === "missing") {
    Message.warning(`主数据集 ${record.primary_dataset_id || ""} 未加载，无法判断视图类型`);
    return;
  }
  if (rebuildKind === "time_series") {
    await rebuildTimeSeriesView({ space_id: spaceId, view_id: record.view_id });
  } else {
    await rebuildRecordView({ space_id: spaceId, view_id: record.view_id });
  }
  Message.success("视图重建任务已提交");
  await load();
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
  if (next.join("|") !== (form.dataset_ids || []).join("|")) {
    form.dataset_ids = next;
    Message.warning("包含数据集不能与主数据集相同，已自动移除");
  }
}

watch(
  () => form.primary_dataset_id,
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
.metadata-page {
  padding: 20px;
}

.page-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 16px;
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
