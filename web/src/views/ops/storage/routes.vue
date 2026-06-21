<template>
  <div class="ops-page">
    <div class="page-head">
      <div>
        <h2>主存路由</h2>
        <span>当前空间：{{ spaceStore.selectedSpace?.name || '未选择' }}</span>
      </div>
      <a-space>
        <a-button type="primary" :disabled="!selectedSpaceId" @click="openCreate">
          <template #icon><icon-plus /></template>
          新增路由
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
      row-key="route_id"
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
        <a-table-column title="路由ID" data-index="route_id" :width="170" />
        <a-table-column title="数据集" data-index="dataset_id" :width="160" />
        <a-table-column title="对象ID" data-index="subject_id" :width="160" />
        <a-table-column title="对象模式" data-index="subject_pattern" :width="160" />
        <a-table-column title="Hash 规则" data-index="hash_rule" :width="140" />
        <a-table-column title="节点ID" data-index="node_id" :width="170" />
        <a-table-column title="优先级" data-index="priority" :width="90" />
        <a-table-column title="状态" :width="90">
          <template #cell="{ record }">
            <a-tag size="small" :color="statusColor(record.status)">{{ record.status }}</a-tag>
          </template>
        </a-table-column>
        <a-table-column title="操作" :width="90" align="center" :fixed="'right'">
          <template #cell="{ record }">
            <a-button size="mini" type="text" @click="openEdit(record)">编辑</a-button>
          </template>
        </a-table-column>
      </template>
    </a-table>

    <a-modal v-model:visible="visible" width="780px" :title="modalTitle" @ok="submit">
      <a-form :model="form" auto-label-width>
        <a-form-item field="route_id" label="路由ID" required>
          <a-input v-model="form.route_id" :disabled="editing" placeholder="例如 route-kline" />
        </a-form-item>
        <a-form-item field="dataset_id" label="数据集" required>
          <a-select v-model="form.dataset_id" allow-search allow-create placeholder="选择或输入 dataset_id">
            <a-option v-for="item in datasets" :key="item.dataset_id" :value="item.dataset_id">
              {{ item.name }} ({{ item.dataset_id }})
            </a-option>
          </a-select>
        </a-form-item>
        <a-form-item field="subject_id" label="对象ID">
          <a-input v-model="form.subject_id" placeholder="精确对象，可留空" />
        </a-form-item>
        <a-form-item field="subject_pattern" label="对象模式">
          <a-input v-model="form.subject_pattern" placeholder="模式匹配，可留空" />
        </a-form-item>
        <a-form-item field="hash_rule" label="Hash 规则">
          <a-input v-model="form.hash_rule" placeholder="例如 subject_id" />
        </a-form-item>
        <a-form-item field="node_id" label="主存节点" required>
          <a-select v-model="form.node_id" allow-search allow-create placeholder="选择或输入 node_id">
            <a-option v-for="item in nodes" :key="item.node_id" :value="item.node_id">
              {{ item.name }} ({{ item.node_id }})
            </a-option>
          </a-select>
        </a-form-item>
        <a-form-item field="priority" label="优先级">
          <a-input-number v-model="form.priority" :min="0" />
        </a-form-item>
        <a-form-item field="status" label="状态">
          <a-select v-model="form.status">
            <a-option v-for="item in statusOptions" :key="item.value" :value="item.value">{{ item.label }}</a-option>
          </a-select>
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue';
import { Message } from '@arco-design/web-vue';
import {
  createPrimaryStoreRoute,
  listDatasets,
  listPrimaryStoreNodes,
  listPrimaryStoreRoutes,
  updatePrimaryStoreRoute,
} from '@/api/storage/metadata';
import type { Dataset, PrimaryStoreNode, PrimaryStoreRoute } from '@/api/storage/types';
import { useSpaceStore } from '@/store/modules/space';
import { applyPageResult, defaultPagination, statusColor, statusOptions } from '@/views/data/shared/metadata-utils';

defineOptions({ name: 'OpsStorageRoutes' });

const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);
const rows = ref<PrimaryStoreRoute[]>([]);
const datasets = ref<Dataset[]>([]);
const nodes = ref<PrimaryStoreNode[]>([]);
const loading = ref(false);
const visible = ref(false);
const editing = ref(false);
const pagination = reactive(defaultPagination());

const form = reactive<PrimaryStoreRoute>({
  space_id: '',
  route_id: '',
  dataset_id: '',
  subject_id: '',
  subject_pattern: '',
  hash_rule: 'subject_id',
  node_id: '',
  priority: 100,
  status: 'active',
});

const modalTitle = computed(() => (editing.value ? '编辑主存路由' : '新增主存路由'));

async function loadOptions() {
  if (!selectedSpaceId.value) {
    datasets.value = [];
    nodes.value = [];
    return;
  }
  const [datasetRsp, nodeRsp] = await Promise.all([
    listDatasets({ space_id: selectedSpaceId.value, page: { page: 1, size: 500 } }),
    listPrimaryStoreNodes({ page: { page: 1, size: 500 } }),
  ]);
  datasets.value = datasetRsp.datasets || [];
  nodes.value = nodeRsp.primary_store_nodes || [];
}

async function load() {
  if (!selectedSpaceId.value) {
    rows.value = [];
    return;
  }
  loading.value = true;
  try {
    await loadOptions();
    const rsp = await listPrimaryStoreRoutes({
      space_id: selectedSpaceId.value,
      page: { page: pagination.current, size: pagination.pageSize },
    });
    rows.value = rsp.primary_store_routes || [];
    applyPageResult(pagination, rsp.page_result);
  } finally {
    loading.value = false;
  }
}

function resetForm() {
  Object.assign(form, {
    space_id: selectedSpaceId.value,
    route_id: '',
    dataset_id: '',
    subject_id: '',
    subject_pattern: '',
    hash_rule: 'subject_id',
    node_id: '',
    priority: 100,
    status: 'active',
  });
}

function openCreate() {
  editing.value = false;
  resetForm();
  visible.value = true;
}

function openEdit(record: PrimaryStoreRoute) {
  editing.value = true;
  Object.assign(form, record);
  visible.value = true;
}

async function submit() {
  const spaceId = spaceStore.requireSpaceId();
  if (!form.route_id || !form.dataset_id || !form.node_id) {
    Message.warning('请补全路由ID、数据集和主存节点');
    return;
  }
  const payload = { ...form, space_id: spaceId };
  if (editing.value) await updatePrimaryStoreRoute(payload);
  else await createPrimaryStoreRoute(payload);
  Message.success('主存路由已保存');
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
.ops-page {
  padding: 20px;
}

.page-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 16px;
}

.page-head h2 {
  margin: 0 0 4px;
  font-size: 20px;
  font-weight: 600;
}

.page-head span {
  color: var(--color-text-3);
}
</style>
