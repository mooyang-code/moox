<template>
  <div class="metadata-page">
    <div class="page-head">
      <h2>数据集</h2>
      <a-space>
        <a-button type="primary" :disabled="!selectedSpaceId" @click="openCreate">
          <template #icon><icon-plus /></template>
          新增数据集
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
      row-key="dataset_id"
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
        <a-table-column title="数据集ID" data-index="dataset_id" :width="180" />
        <a-table-column title="中文名" data-index="name" :width="180" />
        <a-table-column title="数据源" data-index="data_source_id" :width="150" />
        <a-table-column title="数据形态" :width="130">
          <template #cell="{ record }">{{ optionLabel(dataKindOptions, record.data_kind) }}</template>
        </a-table-column>
        <a-table-column title="频率" :width="180">
          <template #cell="{ record }">{{ joinList(record.freqs) || '-' }}</template>
        </a-table-column>
        <a-table-column title="状态" :width="90">
          <template #cell="{ record }">
            <a-tag size="small" :color="statusColor(record.status)">{{ record.status }}</a-tag>
          </template>
        </a-table-column>
        <a-table-column title="更新时间" :width="180">
          <template #cell="{ record }">{{ formatTime(record.updated_at) }}</template>
        </a-table-column>
        <a-table-column title="操作" :width="210" align="center" :fixed="'right'">
          <template #cell="{ record }">
            <a-space>
              <a-button size="mini" type="text" @click="openManage(record)">列/对象</a-button>
              <a-button size="mini" type="text" @click="openEdit(record)">编辑</a-button>
            </a-space>
          </template>
        </a-table-column>
      </template>
    </a-table>

    <a-modal v-model:visible="visible" width="760px" :title="modalTitle" @ok="submit">
      <a-form :model="form" auto-label-width>
        <a-form-item field="dataset_id" label="数据集ID" required>
          <a-input v-model="form.dataset_id" :disabled="editing" placeholder="例如 kline" />
        </a-form-item>
        <a-form-item field="data_source_id" label="数据源ID" required>
          <a-select v-model="form.data_source_id" allow-search allow-create placeholder="选择或输入来源ID">
            <a-option v-for="item in dataSources" :key="item.data_source_id" :value="item.data_source_id">
              {{ item.name }} ({{ item.data_source_id }})
            </a-option>
          </a-select>
        </a-form-item>
        <a-form-item field="name" label="中文名" required>
          <a-input v-model="form.name" :max-length="10" show-word-limit placeholder="例如 现货K线" />
        </a-form-item>
        <a-form-item field="description" label="描述">
          <a-textarea v-model="form.description" :auto-size="{ minRows: 3, maxRows: 5 }" />
        </a-form-item>
        <a-form-item field="data_kind" label="数据形态" required>
          <a-select v-model="form.data_kind">
            <a-option v-for="item in dataKindOptions" :key="item.value" :value="item.value">{{ item.label }}</a-option>
          </a-select>
        </a-form-item>
        <a-form-item field="freqsText" label="频率">
          <a-input-tag v-model="freqTags" allow-clear placeholder="例如 1m、1h、1d" />
        </a-form-item>
        <a-form-item field="status" label="状态">
          <a-select v-model="form.status">
            <a-option v-for="item in statusOptions" :key="item.value" :value="item.value">{{ item.label }}</a-option>
          </a-select>
        </a-form-item>
      </a-form>
    </a-modal>

    <a-drawer v-model:visible="manageVisible" width="920px" :footer="false">
      <template #title>数据集配置：{{ activeDataset?.dataset_id }}</template>
      <a-tabs default-active-key="columns">
        <a-tab-pane key="columns" title="列定义">
          <DatasetColumnPanel :space-id="selectedSpaceId" :dataset-id="activeDataset?.dataset_id || ''" />
        </a-tab-pane>
        <a-tab-pane key="subjects" title="对象绑定">
          <DatasetSubjectPanel :space-id="selectedSpaceId" :dataset-id="activeDataset?.dataset_id || ''" />
        </a-tab-pane>
      </a-tabs>
    </a-drawer>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue';
import { Message } from '@arco-design/web-vue';
import { createDataset, listDatasets, listDataSources, updateDataset } from '@/api/storage/metadata';
import type { DataSource, Dataset } from '@/api/storage/types';
import { useSpaceStore } from '@/store/modules/space';
import DatasetColumnPanel from './components/dataset-column-panel.vue';
import DatasetSubjectPanel from './components/dataset-subject-panel.vue';
import {
  applyPageResult,
  dataKindOptions,
  defaultPagination,
  formatTime,
  joinList,
  optionLabel,
  splitList,
  statusColor,
  statusOptions,
  validateChineseDisplayName,
  validateLowerSnakeId,
} from '@/views/data/shared/metadata-utils';

defineOptions({ name: 'DataDatasets' });

type DatasetForm = Dataset & { freqsText?: string };

const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);
const rows = ref<Dataset[]>([]);
const dataSources = ref<DataSource[]>([]);
const loading = ref(false);
const visible = ref(false);
const editing = ref(false);
const manageVisible = ref(false);
const activeDataset = ref<Dataset>();
const pagination = reactive(defaultPagination());

const form = reactive<DatasetForm>({
  space_id: '',
  dataset_id: '',
  data_source_id: '',
  name: '',
  description: '',
  data_kind: 'DATA_KIND_TIME_SERIES',
  freqs: [],
  freqsText: '',
  status: 'active',
});

const freqTags = computed({
  get: () => form.freqs || [],
  set: (value: string[]) => {
    form.freqs = value;
  },
});

const modalTitle = computed(() => (editing.value ? '编辑数据集' : '新增数据集'));

async function loadDataSources() {
  if (!selectedSpaceId.value) {
    dataSources.value = [];
    return;
  }
  const rsp = await listDataSources({ space_id: selectedSpaceId.value, page: { page: 1, size: 200 } });
  dataSources.value = rsp.data_sources || [];
}

async function load() {
  if (!selectedSpaceId.value) {
    rows.value = [];
    return;
  }
  loading.value = true;
  try {
    await loadDataSources();
    const rsp = await listDatasets({
      space_id: selectedSpaceId.value,
      page: { page: pagination.current, size: pagination.pageSize },
    });
    rows.value = rsp.datasets || [];
    applyPageResult(pagination, rsp.page_result);
  } finally {
    loading.value = false;
  }
}

function resetForm() {
  Object.assign(form, {
    space_id: selectedSpaceId.value,
    dataset_id: '',
    data_source_id: '',
    name: '',
    description: '',
    data_kind: 'DATA_KIND_TIME_SERIES',
    freqs: [],
    freqsText: '',
    status: 'active',
  });
}

function openCreate() {
  editing.value = false;
  resetForm();
  visible.value = true;
}

function openEdit(record: Dataset) {
  editing.value = true;
  Object.assign(form, {
    ...record,
    freqs: record.freqs || [],
    freqsText: joinList(record.freqs),
  });
  visible.value = true;
}

function openManage(record: Dataset) {
  activeDataset.value = record;
  manageVisible.value = true;
}

async function submit() {
  const spaceId = spaceStore.requireSpaceId();
  if (!form.dataset_id || !form.data_source_id || !form.name || !form.data_kind) {
    Message.warning('请补全数据集ID、数据源、中文名和数据形态');
    return;
  }
  const nameError = validateChineseDisplayName(form.name);
  if (nameError) {
    Message.warning(nameError);
    return;
  }
  const idError = validateLowerSnakeId(form.dataset_id, 20);
  if (idError) {
    Message.warning(`数据集${idError}`);
    return;
  }
  const payload: Dataset = {
    space_id: spaceId,
    dataset_id: form.dataset_id,
    data_source_id: form.data_source_id,
    name: form.name,
    description: form.description,
    data_kind: form.data_kind,
    freqs: splitList(form.freqs),
    status: form.status,
  };
  if (editing.value) await updateDataset(payload);
  else await createDataset(payload);
  Message.success('数据集已保存');
  visible.value = false;
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
