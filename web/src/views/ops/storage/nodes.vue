<template>
  <div class="ops-page">
    <div class="page-head">
      <h2>主存节点</h2>
      <a-space>
        <a-button type="primary" @click="openCreate">
          <template #icon><icon-plus /></template>
          新增节点
        </a-button>
        <a-button @click="load">
          <template #icon><icon-refresh /></template>
          刷新
        </a-button>
      </a-space>
    </div>

    <a-alert class="topology-alert" type="warning" show-icon>
      主存节点是 storage 数据拓扑配置，不等同于系统服务部署信息。若在“系统设置 / 服务部署信息”修改了 storage_* 服务 IP/端口，请同步检查这里的 Endpoint。
    </a-alert>

    <a-table
      row-key="node_id"
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
        <a-table-column title="节点ID" data-index="node_id" :width="180" />
        <a-table-column title="名称" data-index="name" :width="180" />
        <a-table-column title="Endpoint" data-index="endpoint" :width="260" />
        <a-table-column title="权重" data-index="weight" :width="90" />
        <a-table-column title="状态" :width="90">
          <template #cell="{ record }">
            <a-tag size="small" :color="statusColor(record.status)">{{ record.status }}</a-tag>
          </template>
        </a-table-column>
        <a-table-column title="更新时间" :width="180">
          <template #cell="{ record }">{{ formatTime(record.updated_at) }}</template>
        </a-table-column>
        <a-table-column title="操作" :width="90" align="center" :fixed="'right'">
          <template #cell="{ record }">
            <a-button size="mini" type="text" @click="openEdit(record)">编辑</a-button>
          </template>
        </a-table-column>
      </template>
    </a-table>

    <a-modal v-model:visible="visible" width="760px" :title="modalTitle" @ok="submit">
      <a-form :model="form" auto-label-width>
        <a-form-item field="node_id" label="节点ID" required>
          <a-input v-model="form.node_id" :disabled="editing" placeholder="例如 primary-local" />
        </a-form-item>
        <a-form-item field="name" label="名称" required>
          <a-input v-model="form.name" />
        </a-form-item>
        <a-form-item field="endpoint" label="Endpoint" required>
          <a-input v-model="form.endpoint" placeholder="例如 pebble://local 或 trpc://host:port" />
        </a-form-item>
        <a-form-item field="weight" label="权重">
          <a-input-number v-model="form.weight" :min="0" />
        </a-form-item>
        <a-form-item field="config_json" label="配置JSON">
          <a-textarea v-model="form.config_json" :auto-size="{ minRows: 4, maxRows: 8 }" />
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
import { computed, onMounted, reactive, ref } from 'vue';
import { Message } from '@arco-design/web-vue';
import { createPrimaryStoreNode, listPrimaryStoreNodes, updatePrimaryStoreNode } from '@/api/storage/metadata';
import type { PrimaryStoreNode } from '@/api/storage/types';
import { applyPageResult, defaultPagination, formatTime, jsonText, statusColor, statusOptions } from '@/views/data/shared/metadata-utils';

defineOptions({ name: 'OpsStorageNodes' });

const rows = ref<PrimaryStoreNode[]>([]);
const loading = ref(false);
const visible = ref(false);
const editing = ref(false);
const pagination = reactive(defaultPagination());

const form = reactive<PrimaryStoreNode>({
  node_id: '',
  name: '',
  endpoint: '',
  weight: 100,
  status: 'active',
  config_json: '{}',
});

const modalTitle = computed(() => (editing.value ? '编辑主存节点' : '新增主存节点'));

async function load() {
  loading.value = true;
  try {
    const rsp = await listPrimaryStoreNodes({ page: { page: pagination.current, size: pagination.pageSize } });
    rows.value = rsp.nodes || [];
    applyPageResult(pagination, rsp.page_result);
  } finally {
    loading.value = false;
  }
}

function resetForm() {
  Object.assign(form, {
    node_id: '',
    name: '',
    endpoint: '',
    weight: 100,
    status: 'active',
    config_json: '{}',
  });
}

function openCreate() {
  editing.value = false;
  resetForm();
  visible.value = true;
}

function openEdit(record: PrimaryStoreNode) {
  editing.value = true;
  Object.assign(form, {
    ...record,
    config_json: jsonText(record.config_json),
  });
  visible.value = true;
}

async function submit() {
  if (!form.node_id || !form.name || !form.endpoint) {
    Message.warning('请补全节点ID、名称和 Endpoint');
    return;
  }
  const payload = { ...form, config_json: jsonText(form.config_json) };
  if (editing.value) await updatePrimaryStoreNode(payload);
  else await createPrimaryStoreNode(payload);
  Message.success('主存节点已保存');
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
  margin: 0;
  font-size: 20px;
  font-weight: 600;
}

.topology-alert {
  margin-bottom: 14px;
}
</style>
