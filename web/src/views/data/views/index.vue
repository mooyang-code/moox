<template>
  <div class="metadata-page">
    <div class="page-head">
      <h2>查询视图</h2>
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
            <a-tag size="small" :color="statusColor(record.build_status)">{{ record.build_status || '-' }}</a-tag>
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
            <a-option v-for="item in datasets" :key="item.dataset_id" :value="item.dataset_id">
              {{ item.name }} ({{ item.dataset_id }})
            </a-option>
          </a-select>
        </a-form-item>
        <a-form-item field="grainKeys" label="粒度键">
          <a-input-tag v-model="grainTags" allow-clear placeholder="例如 subject_id、freq、data_time" />
        </a-form-item>
        <a-form-item field="engine" label="引擎">
          <a-input v-model="form.engine" placeholder="duckdb 或 bleve" />
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
import { computed, onMounted, reactive, ref, watch } from 'vue';
import { Message } from '@arco-design/web-vue';
import { createView, listDatasets, listViews, updateView } from '@/api/storage/metadata';
import { rebuildRecordView, rebuildTimeSeriesView } from '@/api/storage/view';
import type { Dataset, View } from '@/api/storage/types';
import { useSpaceStore } from '@/store/modules/space';
import ViewColumnPanel from './components/view-column-panel.vue';
import { applyPageResult, defaultPagination, formatTime, isTimeSeriesDataKind, jsonText, splitList, statusColor, statusOptions } from '@/views/data/shared/metadata-utils';

defineOptions({ name: 'DataViews' });

type ViewForm = View & { grainKeysText?: string };

const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);
const rows = ref<View[]>([]);
const datasets = ref<Dataset[]>([]);
const loading = ref(false);
const visible = ref(false);
const editing = ref(false);
const columnsVisible = ref(false);
const activeView = ref<View>();
const pagination = reactive(defaultPagination());

const form = reactive<ViewForm>({
  space_id: '',
  view_id: '',
  name: '',
  description: '',
  primary_dataset_id: '',
  dataset_ids: [],
  grain_keys: [],
  grainKeysText: '',
  filter_json: '{}',
  engine: '',
  query_window: '',
  status: 'active',
});

const grainTags = computed({
  get: () => form.grain_keys || [],
  set: (value: string[]) => {
    form.grain_keys = value;
  },
});

const modalTitle = computed(() => (editing.value ? '编辑视图' : '新增视图'));

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
      page: { page: pagination.current, size: pagination.pageSize },
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
    view_id: '',
    name: '',
    description: '',
    primary_dataset_id: '',
    dataset_ids: [],
    grain_keys: [],
    grainKeysText: '',
    filter_json: '{}',
    engine: '',
    query_window: '',
    status: 'active',
  });
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
    dataset_ids: record.dataset_ids || [],
    grain_keys: record.grain_keys || [],
    grainKeysText: (record.grain_keys || []).join(','),
    filter_json: jsonText(record.filter_json),
  });
  visible.value = true;
}

function openColumns(record: View) {
  activeView.value = record;
  columnsVisible.value = true;
}

async function submit() {
  const spaceId = spaceStore.requireSpaceId();
  if (!form.view_id || !form.name || !form.primary_dataset_id) {
    Message.warning('请补全视图ID、名称和主数据集');
    return;
  }
  const datasetIds = form.dataset_ids?.length ? form.dataset_ids : [form.primary_dataset_id];
  const payload: View = {
    space_id: spaceId,
    view_id: form.view_id,
    name: form.name,
    description: form.description,
    primary_dataset_id: form.primary_dataset_id,
    dataset_ids: datasetIds,
    grain_keys: splitList(form.grain_keys),
    filter_json: jsonText(form.filter_json),
    engine: form.engine,
    query_window: form.query_window,
    status: form.status,
  };
  if (editing.value) await updateView(payload);
  else await createView(payload);
  Message.success('视图已保存');
  visible.value = false;
  await load();
}

async function rebuild(record: View) {
  const spaceId = spaceStore.requireSpaceId();
  const primaryDataset = datasets.value.find((item) => item.dataset_id === record.primary_dataset_id);
  if (isTimeSeriesDataKind(primaryDataset?.data_kind)) {
    await rebuildTimeSeriesView({ space_id: spaceId, view_id: record.view_id });
  } else {
    await rebuildRecordView({ space_id: spaceId, view_id: record.view_id });
  }
  Message.success('视图重建任务已提交');
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
</style>
