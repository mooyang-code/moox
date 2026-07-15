<template>
  <div class="gateway-nodes">
    <div class="toolbar">
      <a-space wrap>
        <a-input v-model="filters.node_id" allow-clear placeholder="节点 ID" @press-enter="reloadFirstPage" />
        <a-select v-model="filters.status" allow-clear placeholder="配置状态" class="status-filter" @change="reloadFirstPage">
          <a-option value="enabled">enabled</a-option>
          <a-option value="disabled">disabled</a-option>
        </a-select>
        <a-button @click="reloadFirstPage">查询</a-button>
      </a-space>
      <a-space>
        <a-tooltip content="刷新节点状态">
          <a-button aria-label="刷新节点状态" @click="load">
            <template #icon><icon-refresh /></template>
          </a-button>
        </a-tooltip>
        <a-button type="primary" @click="openCreate">
          <template #icon><icon-plus /></template>
          新增节点
        </a-button>
      </a-space>
    </div>

    <a-table
      row-key="node_id"
      size="small"
      :bordered="{ cell: true }"
      :loading="loading"
      :data="rows"
      :pagination="pagination"
      @page-change="onPageChange"
      @page-size-change="onPageSizeChange"
    >
      <template #columns>
        <a-table-column title="节点" :width="190">
          <template #cell="{ record }">
            <div class="node-identity">
              <div class="node-name"><strong>{{ record.name || record.node_id }}</strong><a-tag size="small" :color="record.status === 'enabled' ? 'blue' : 'gray'">{{ record.status }}</a-tag></div>
              <span>{{ record.node_id }}</span>
            </div>
          </template>
        </a-table-column>
        <a-table-column title="关联主机" :width="150">
          <template #cell="{ record }">{{ hostLabel(record.host_id) }}</template>
        </a-table-column>
        <a-table-column title="公开地址" data-index="public_address" :width="230" ellipsis tooltip />
        <a-table-column title="在线状态" :width="100">
          <template #cell="{ record }">
            <a-tag size="small" :color="onlineColor(record.last_seen_at)">{{ onlineLabel(record.last_seen_at) }}</a-tag>
          </template>
        </a-table-column>
        <a-table-column title="最后心跳" :width="165">
          <template #cell="{ record }">{{ formatTime(record.last_seen_at) }}</template>
        </a-table-column>
        <a-table-column title="路由" :width="80" align="right">
          <template #cell="{ record }">{{ record.route_count || 0 }}</template>
        </a-table-column>
        <a-table-column title="路由摘要" :width="190">
          <template #cell="{ record }">
            <a-tooltip :content="hashTooltip(record)">
              <div class="hash-state">
                <a-tag size="small" :color="hashColor(record)">{{ hashLabel(record) }}</a-tag>
                <div class="hash-values"><code>预期 {{ shortHash(record.route_hash) }}</code><code>应用 {{ shortHash(record.applied_route_hash) }}</code></div>
              </div>
            </a-tooltip>
          </template>
        </a-table-column>
        <a-table-column title="最近错误" :width="180" ellipsis tooltip>
          <template #cell="{ record }"><span :class="{ 'error-text': record.last_error }">{{ record.last_error || '--' }}</span></template>
        </a-table-column>
        <a-table-column title="操作" :width="142" align="center" :fixed="'right'">
          <template #cell="{ record }">
            <a-space :size="4">
              <a-tooltip content="查看路由">
                <a-button size="mini" type="text" :aria-label="`查看 ${record.name || record.node_id} 路由`" @click="openRoutes(record)">
                  <template #icon><icon-eye /></template>
                </a-button>
              </a-tooltip>
              <a-tooltip content="编辑节点">
                <a-button size="mini" type="text" :aria-label="`编辑 ${record.name || record.node_id}`" @click="openEdit(record)">
                  <template #icon><icon-edit /></template>
                </a-button>
              </a-tooltip>
              <a-popconfirm content="确认删除该网关节点？" @ok="remove(record)">
                <a-tooltip content="删除节点">
                  <a-button size="mini" type="text" status="danger" :aria-label="`删除 ${record.name || record.node_id}`">
                    <template #icon><icon-delete /></template>
                  </a-button>
                </a-tooltip>
              </a-popconfirm>
            </a-space>
          </template>
        </a-table-column>
      </template>
    </a-table>

    <a-modal v-model:visible="editorVisible" width="620px" :title="editing ? '编辑网关节点' : '新增网关节点'" @ok="submit">
      <a-form :model="form" layout="vertical" class="node-form">
        <a-form-item field="node_id" label="节点 ID" required>
          <a-input v-model="form.node_id" :disabled="editing" placeholder="例如 gateway-gz-122" />
        </a-form-item>
        <a-form-item field="name" label="节点名称" required><a-input v-model="form.name" /></a-form-item>
        <a-form-item field="host_id" label="关联主机" required>
          <a-select v-model="form.host_id" placeholder="选择主机">
            <a-option v-for="host in hosts" :key="host.id" :value="host.id">{{ host.name }} ({{ host.address }})</a-option>
          </a-select>
        </a-form-item>
        <a-form-item field="status" label="配置状态">
          <a-select v-model="form.status"><a-option value="enabled">enabled</a-option><a-option value="disabled">disabled</a-option></a-select>
        </a-form-item>
        <a-form-item class="form-span-2" field="public_address" label="Gateway HTTPS 地址" required>
          <a-input v-model="form.public_address" placeholder="https://gateway.example.com" />
        </a-form-item>
      </a-form>
    </a-modal>

    <a-modal v-model:visible="routesVisible" width="900px" title="节点路由" :footer="false">
      <div class="routes-meta">
        <span>节点：<strong>{{ routeSnapshot.node_id }}</strong></span>
        <span>生成时间：{{ formatTime(routeSnapshot.generated_at) }}</span>
        <a-tag v-if="routeSnapshot.disabled" size="small" color="gray">节点已停用</a-tag>
      </div>
      <a-table row-key="service_id" size="small" :bordered="{ cell: true }" :data="routeSnapshot.routes" :pagination="false">
        <template #columns>
          <a-table-column title="Service ID" data-index="service_id" :width="150" />
          <a-table-column title="本机地址" data-index="address" :width="170" />
          <a-table-column title="tRPC Path" data-index="service_path" :width="230" ellipsis tooltip />
          <a-table-column title="允许方法" :width="200">
            <template #cell="{ record }">{{ record.allowed_methods?.join(', ') || '全部' }}</template>
          </a-table-column>
        </template>
      </a-table>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue';
import { Message } from '@arco-design/web-vue';
import { createGatewayNode, deleteGatewayNode, getGatewayNodeRoutes, listGatewayNodes, updateGatewayNode } from '@/api/admin/sysdeploy';
import type { GatewayNode, GatewayNodeInput, GatewayRoute } from '@/api/admin/types';
import { listSSHHosts, type SSHHost } from '@/api/modules/ssh';
import { applyPageResult, defaultPagination, formatTime } from '@/views/data/shared/metadata-utils';
import { gatewayHashState, gatewayNodeOnlineState } from '@/views/settings/service-deployments/health';

const rows = ref<GatewayNode[]>([]);
const hosts = ref<SSHHost[]>([]);
const loading = ref(false);
const editorVisible = ref(false);
const routesVisible = ref(false);
const editing = ref(false);
const editingNodeID = ref('');
const pagination = reactive(defaultPagination());
const filters = reactive({ node_id: '', status: '' });
const form = reactive<GatewayNodeInput>({ node_id: '', host_id: 0, name: '', public_address: '', status: 'enabled' });
const routeSnapshot = reactive<{ node_id: string; generated_at: string; disabled: boolean; routes: GatewayRoute[] }>({
  node_id: '', generated_at: '', disabled: false, routes: [],
});

async function load() {
  loading.value = true;
  try {
    const [nodeRsp, hostRsp] = await Promise.all([
      listGatewayNodes({ node_id: filters.node_id || undefined, status: filters.status || undefined, page: { page: pagination.current, size: pagination.pageSize } }),
      listSSHHosts({ limit: 500 }),
    ]);
    rows.value = nodeRsp.nodes || [];
    hosts.value = hostRsp.hosts || [];
    applyPageResult(pagination, nodeRsp.page_result);
  } finally {
    loading.value = false;
  }
}

function resetForm() { Object.assign(form, { node_id: '', host_id: 0, name: '', public_address: '', status: 'enabled' }); }
function openCreate() { editing.value = false; editingNodeID.value = ''; resetForm(); editorVisible.value = true; }
function openEdit(node: GatewayNode) {
  editing.value = true;
  editingNodeID.value = node.node_id;
  Object.assign(form, { node_id: node.node_id, host_id: node.host_id, name: node.name, public_address: node.public_address, status: node.status });
  editorVisible.value = true;
}
async function submit() {
  if (!form.node_id.trim() || !form.name.trim() || !form.host_id || !form.public_address.trim()) {
    Message.warning('请补全节点 ID、名称、关联主机和公开地址'); return;
  }
  if (!/^https:\/\//i.test(form.public_address)) { Message.warning('Gateway 公开地址必须使用 HTTPS'); return; }
  if (editing.value) await updateGatewayNode(editingNodeID.value, { ...form });
  else await createGatewayNode({ ...form });
  Message.success('网关节点已保存'); editorVisible.value = false; await load();
}
async function remove(node: GatewayNode) { await deleteGatewayNode(node.node_id); Message.success('网关节点已删除'); await load(); }
async function openRoutes(node: GatewayNode) {
  const rsp = await getGatewayNodeRoutes(node.node_id);
  Object.assign(routeSnapshot, { node_id: rsp.node_id, generated_at: rsp.generated_at, disabled: rsp.disabled, routes: rsp.routes || [] });
  routesVisible.value = true;
}
function reloadFirstPage() { pagination.current = 1; void load(); }
function onPageChange(page: number) { pagination.current = page; void load(); }
function onPageSizeChange(size: number) { pagination.current = 1; pagination.pageSize = size; void load(); }
function hostLabel(hostID: number) { const host = hosts.value.find((item) => item.id === hostID); return host ? `${host.name} (${host.address})` : hostID ? `#${hostID}` : '--'; }
function onlineLabel(lastSeenAt?: string) { return gatewayNodeOnlineState(lastSeenAt).label; }
function onlineColor(lastSeenAt?: string) { return { online: 'green', offline: 'red', never: 'gray' }[gatewayNodeOnlineState(lastSeenAt).state]; }
function hashLabel(node: GatewayNode) { return gatewayHashState(node.route_hash, node.applied_route_hash).label; }
function hashColor(node: GatewayNode) { return gatewayHashState(node.route_hash, node.applied_route_hash).state === 'synced' ? 'green' : 'orange'; }
function hashTooltip(node: GatewayNode) { return `预期：${node.route_hash || '--'}\n已应用：${node.applied_route_hash || '--'}`; }
function shortHash(hash?: string) { return hash ? hash.slice(0, 10) : '--'; }

onMounted(load);
</script>

<style scoped>
.gateway-nodes { min-width: 0; }
.toolbar { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 14px; }
.status-filter { width: 140px; }
.node-identity { display: flex; min-width: 0; flex-direction: column; gap: 2px; }
.node-name { display: flex; min-width: 0; align-items: center; gap: 6px; }
.node-identity strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.node-identity span, .hash-state code { color: var(--color-text-3); font-size: 12px; }
.hash-state { display: flex; align-items: center; gap: 8px; }
.hash-values { display: flex; min-width: 0; flex-direction: column; }
.error-text { color: rgb(var(--danger-6)); }
.node-form { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px 16px; }
.node-form :deep(.arco-form-item) { margin-bottom: 0; }
.form-span-2 { grid-column: 1 / -1; }
.routes-meta { display: flex; align-items: center; gap: 20px; margin-bottom: 14px; color: var(--color-text-2); }
@media (max-width: 768px) {
  .toolbar { align-items: stretch; flex-direction: column; }
  .node-form { grid-template-columns: 1fr; }
  .form-span-2 { grid-column: auto; }
}
@media (max-width: 1280px) {
  .gateway-nodes :deep(th:nth-child(5)),
  .gateway-nodes :deep(td:nth-child(5)),
  .gateway-nodes :deep(th:nth-child(8)),
  .gateway-nodes :deep(td:nth-child(8)) { display: none; }
}
@media (max-width: 900px) {
  .gateway-nodes :deep(th:nth-child(2)),
  .gateway-nodes :deep(td:nth-child(2)),
  .gateway-nodes :deep(th:nth-child(3)),
  .gateway-nodes :deep(td:nth-child(3)),
  .gateway-nodes :deep(th:nth-child(6)),
  .gateway-nodes :deep(td:nth-child(6)) { display: none; }
}
</style>
