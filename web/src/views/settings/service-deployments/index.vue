<template>
  <div class="admin-page">
    <div class="page-head">
      <div>
        <h2>服务部署</h2>
        <span>统一维护 admin、service、storage、trade 等服务的访问地址；SCF 运行时由 keepalive 动态获取。</span>
      </div>
      <a-space>
        <a-button type="primary" @click="openCreate">
          <template #icon><icon-plus /></template>
          新增服务
        </a-button>
        <a-button @click="load">
          <template #icon><icon-refresh /></template>
          刷新
        </a-button>
      </a-space>
    </div>

    <a-alert class="top-alert" type="warning" show-icon>
      storage_* 部署地址与“主存节点”拓扑可能指向同一组机器；修改 storage 服务 IP/端口后，请同步检查 /#/ops/storage/nodes 的 Endpoint。
    </a-alert>

    <a-space class="filters" wrap>
      <a-input v-model="filters.service_name" allow-clear placeholder="服务名" @press-enter="reloadFirstPage" />
      <a-select v-model="filters.service_kind" allow-clear placeholder="服务类型" style="width: 150px" @change="reloadFirstPage">
        <a-option v-for="item in kindOptions" :key="item" :value="item">{{ item }}</a-option>
      </a-select>
      <a-select v-model="filters.scope" allow-clear placeholder="作用域" style="width: 130px" @change="reloadFirstPage">
        <a-option value="public">public</a-option>
        <a-option value="internal">internal</a-option>
      </a-select>
      <a-select v-model="filters.status" allow-clear placeholder="状态" style="width: 130px" @change="reloadFirstPage">
        <a-option value="active">active</a-option>
        <a-option value="disabled">disabled</a-option>
      </a-select>
      <a-button @click="reloadFirstPage">查询</a-button>
    </a-space>

    <a-table
      class="service-deployments-table"
      row-key="service_name"
      size="small"
      :bordered="{ cell: true }"
      :loading="loading"
      :data="rows"
      :pagination="pagination"
      :scroll="{ x: 'max-content', y: tableBodyHeight }"
      @page-change="onPageChange"
      @page-size-change="onPageSizeChange"
    >
      <template #columns>
        <a-table-column title="服务名" data-index="service_name" :width="170" />
        <a-table-column title="类型" data-index="service_kind" :width="110" />
        <a-table-column title="作用域" data-index="scope" :width="90" />
        <a-table-column title="协议" data-index="protocol" :width="80" />
        <a-table-column title="Host" data-index="host" :width="150" />
        <a-table-column title="端口" data-index="port" :width="90" />
        <a-table-column title="Base URL" data-index="base_url" :width="230" />
        <a-table-column title="网关/RPC Path" data-index="gateway_path" :width="230" />
        <a-table-column title="状态" :width="90">
          <template #cell="{ record }">
            <a-tag size="small" :color="statusColor(record.status)">{{ record.status }}</a-tag>
          </template>
        </a-table-column>
        <a-table-column title="说明" data-index="description" :width="260" />
        <a-table-column title="更新时间" :width="180">
          <template #cell="{ record }">{{ formatTime(record.updated_at) }}</template>
        </a-table-column>
        <a-table-column title="操作" :width="150" align="center" :fixed="'right'">
          <template #cell="{ record }">
            <a-space>
              <a-button size="mini" type="text" @click="openEdit(record)">编辑</a-button>
              <a-popconfirm content="确认删除该服务部署信息？" @ok="remove(record)">
                <a-button size="mini" type="text" status="danger">删除</a-button>
              </a-popconfirm>
            </a-space>
          </template>
        </a-table-column>
      </template>
    </a-table>

    <a-modal
      v-model:visible="visible"
      width="760px"
      :title="modalTitle"
      :align-center="false"
      :top="'48px'"
      :modal-style="{ maxWidth: 'calc(100vw - 32px)' }"
      :body-style="{ maxHeight: 'calc(100vh - 176px)', overflowY: 'auto', padding: '18px 24px 14px' }"
      @ok="submit"
    >
      <a-form class="deployment-form" :model="form" layout="vertical">
        <a-form-item field="service_name" label="服务名" required>
          <a-input v-model="form.service_name" :disabled="editing" placeholder="例如 storage_access" />
        </a-form-item>
        <a-form-item field="service_kind" label="服务类型" required>
          <a-input v-model="form.service_kind" placeholder="gateway/storage/admin_rpc/frontend/trade" />
        </a-form-item>
        <a-form-item field="scope" label="作用域">
          <a-select v-model="form.scope">
            <a-option value="public">public</a-option>
            <a-option value="internal">internal</a-option>
          </a-select>
        </a-form-item>
        <a-form-item field="status" label="状态">
          <a-select v-model="form.status">
            <a-option value="active">active</a-option>
            <a-option value="disabled">disabled</a-option>
          </a-select>
        </a-form-item>
        <a-form-item field="protocol" label="协议">
          <a-input v-model="form.protocol" placeholder="http" />
        </a-form-item>
        <a-form-item field="host" label="Host" required>
          <a-input v-model="form.host" placeholder="例如 106.53.107.122" />
        </a-form-item>
        <a-form-item field="port" label="端口" required>
          <a-input-number v-model="form.port" :min="1" :max="65535" />
        </a-form-item>
        <a-form-item class="form-span-2" field="base_url" label="Base URL">
          <a-input v-model="form.base_url" placeholder="留空时按 protocol://host:port 生成" />
        </a-form-item>
        <a-form-item class="form-span-2" field="rpc_address" label="RPC 地址">
          <a-input v-model="form.rpc_address" placeholder="留空时按 host:port 生成" />
        </a-form-item>
        <a-form-item class="form-span-2" field="gateway_path" label="网关/RPC Path">
          <a-input v-model="form.gateway_path" placeholder="例如 /api/service 或 trpc.moox.storage.Access" />
        </a-form-item>
        <a-form-item class="form-span-2" field="description" label="说明">
          <a-textarea v-model="form.description" :auto-size="{ minRows: 2, maxRows: 4 }" />
        </a-form-item>
        <a-form-item class="form-span-2" field="extra_config" label="扩展 JSON">
          <a-textarea v-model="form.extra_config" :auto-size="{ minRows: 2, maxRows: 5 }" />
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue';
import { Message } from '@arco-design/web-vue';
import { createServiceDeployment, deleteServiceDeployment, listServiceDeployments, updateServiceDeployment } from '@/api/admin/sysdeploy';
import type { ServiceDeployment } from '@/api/admin/types';
import { applyPageResult, defaultPagination, formatTime, statusColor } from '@/views/data/shared/metadata-utils';

defineOptions({ name: 'SettingsServiceDeployments' });

const kindOptions = ['gateway', 'frontend', 'storage', 'storage_rpc', 'admin_rpc', 'service_api', 'trade'];
const rows = ref<ServiceDeployment[]>([]);
const loading = ref(false);
const visible = ref(false);
const editing = ref(false);
const editingServiceName = ref('');
const pagination = reactive(defaultPagination());
const filters = reactive({ service_name: '', service_kind: '', scope: '', status: '' });
const viewportHeight = ref(typeof window === 'undefined' ? 900 : window.innerHeight);
const tableBodyHeight = computed(() => Math.max(320, viewportHeight.value - 440));

const form = reactive<ServiceDeployment>({
  service_name: '',
  service_kind: 'service',
  protocol: 'http',
  host: '',
  port: 0,
  base_url: '',
  rpc_address: '',
  gateway_path: '',
  scope: 'public',
  status: 'active',
  description: '',
  extra_config: '{}',
});

const modalTitle = computed(() => (editing.value ? '编辑服务部署' : '新增服务部署'));

async function load() {
  loading.value = true;
  try {
    const rsp = await listServiceDeployments({
      service_name: filters.service_name || undefined,
      service_kind: filters.service_kind || undefined,
      scope: filters.scope || undefined,
      status: filters.status || undefined,
      page: { page: pagination.current, size: pagination.pageSize },
    });
    rows.value = rsp.deployments || [];
    applyPageResult(pagination, rsp.page_result);
  } finally {
    loading.value = false;
  }
}

function resetForm() {
  Object.assign(form, {
    service_name: '',
    service_kind: 'service',
    protocol: 'http',
    host: '',
    port: 0,
    base_url: '',
    rpc_address: '',
    gateway_path: '',
    scope: 'public',
    status: 'active',
    description: '',
    extra_config: '{}',
  });
}

function openCreate() {
  editing.value = false;
  editingServiceName.value = '';
  resetForm();
  visible.value = true;
}

function openEdit(record: ServiceDeployment) {
  editing.value = true;
  editingServiceName.value = record.service_name;
  Object.assign(form, { ...record, extra_config: record.extra_config || '{}' });
  visible.value = true;
}

async function submit() {
  if (!form.service_name || !form.host || !form.port) {
    Message.warning('请补全服务名、Host 和端口');
    return;
  }
  const payload = { ...form, extra_config: form.extra_config || '{}' };
  if (editing.value) await updateServiceDeployment(editingServiceName.value, payload);
  else await createServiceDeployment(payload);
  if (payload.service_name.startsWith('storage_')) {
    Message.warning('服务部署已保存；storage 变更后请同步检查主存节点拓扑');
  } else {
    Message.success('服务部署信息已保存');
  }
  visible.value = false;
  await load();
}

async function remove(record: ServiceDeployment) {
  await deleteServiceDeployment(record.service_name);
  Message.success('服务部署信息已删除');
  await load();
}

function reloadFirstPage() {
  pagination.current = 1;
  load();
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

function updateViewportHeight() {
  viewportHeight.value = window.innerHeight;
}

onMounted(() => {
  updateViewportHeight();
  window.addEventListener('resize', updateViewportHeight);
  load();
});

onUnmounted(() => {
  window.removeEventListener('resize', updateViewportHeight);
});
</script>

<style scoped>
.admin-page {
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

.top-alert {
  margin-bottom: 14px;
}

.service-deployments-table {
  margin-bottom: 20px;
}

.deployment-form {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  column-gap: 16px;
  row-gap: 10px;
  padding-bottom: 0;
}

.form-span-2 {
  grid-column: 1 / -1;
}

.deployment-form :deep(.arco-form-item) {
  margin-bottom: 0;
}

.deployment-form :deep(.arco-form-item-label-col) {
  margin-bottom: 4px;
}

.deployment-form :deep(.arco-input-number) {
  width: 100%;
}

.deployment-form :deep(.arco-textarea-wrapper textarea) {
  resize: vertical;
}

@media (max-width: 768px) {
  .deployment-form {
    grid-template-columns: 1fr;
  }

  .form-span-2 {
    grid-column: auto;
  }
}

.filters {
  margin-bottom: 14px;
}
</style>
