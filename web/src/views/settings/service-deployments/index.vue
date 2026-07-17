<template>
  <div class="moox-page">
    <div class="moox-inner">
      <a-space class="filters" wrap>
        <a-button type="primary" status="success" @click="openCreate">
          <template #icon><icon-plus /></template>
          新增实例
        </a-button>
        <a-select v-model="filters.node_id" allow-clear placeholder="网关节点" style="width: 160px" @change="reloadFirstPage">
          <a-option v-for="node in nodes" :key="node.node_id" :value="node.node_id"
            >{{ node.name }} ({{ node.node_id }})</a-option
          >
        </a-select>
        <a-input
          v-model="filters.service_name"
          allow-clear
          placeholder="服务名"
          style="width: 160px"
          @press-enter="reloadFirstPage"
        />
        <a-select
          v-model="filters.service_kind"
          allow-clear
          placeholder="服务类型"
          style="width: 130px"
          @change="reloadFirstPage"
        >
          <a-option v-for="item in kindOptions" :key="item" :value="item">{{ item }}</a-option>
        </a-select>
        <a-select v-model="filters.scope" allow-clear placeholder="作用域" style="width: 110px" @change="reloadFirstPage">
          <a-option value="public">public</a-option>
          <a-option value="internal">internal</a-option>
        </a-select>
        <a-select v-model="filters.status" allow-clear placeholder="状态" style="width: 110px" @change="reloadFirstPage">
          <a-option value="active">active</a-option>
          <a-option value="disabled">disabled</a-option>
        </a-select>
        <a-select
          v-model="filters.gateway_enabled"
          allow-clear
          placeholder="Gateway 暴露"
          style="width: 130px"
          @change="reloadFirstPage"
        >
          <a-option :value="true">已暴露</a-option><a-option :value="false">未暴露</a-option>
        </a-select>
        <a-button type="primary" @click="reloadFirstPage">
          <template #icon><icon-search /></template>
          查询
        </a-button>
      </a-space>

      <a-table
        class="service-deployments-table"
        :row-key="serviceDeploymentRowKey"
        size="small"
        :bordered="{ cell: true }"
        :loading="loading"
        :data="rows"
        :pagination="pagination"
        :scroll="{ y: tableBodyHeight }"
        @page-change="onPageChange"
        @page-size-change="onPageSizeChange"
      >
        <template #columns>
          <a-table-column title="节点" :width="150" ellipsis tooltip>
            <template #cell="{ record }">{{ nodeLabel(record.node_id) }}</template>
          </a-table-column>
          <a-table-column title="服务实例" :width="180">
            <template #cell="{ record }"
              ><div class="instance-name">
                <strong>{{ record.service_name }}</strong
                ><span>{{ record.service_kind }}</span>
              </div></template
            >
          </a-table-column>
          <a-table-column title="本机地址" :width="175">
            <template #cell="{ record }"
              ><code>{{ record.host }}:{{ record.port }}</code></template
            >
          </a-table-column>
          <a-table-column title="Gateway" :width="195">
            <template #cell="{ record }">
              <div class="gateway-cell">
                <a-tag size="small" :color="record.gateway_enabled ? 'green' : 'gray'">{{
                  record.gateway_enabled ? "已暴露" : "未暴露"
                }}</a-tag>
                <strong>{{ record.gateway_service_id || "--" }}</strong>
                <a-tooltip v-if="record.gateway_path" :content="record.gateway_path"
                  ><span>{{ record.gateway_path }}</span></a-tooltip
                >
              </div>
            </template>
          </a-table-column>
          <a-table-column title="配置状态" :width="100">
            <template #cell="{ record }">
              <a-tag size="small" :color="statusColor(record.status)">{{ record.status }}</a-tag>
            </template>
          </a-table-column>
          <a-table-column class="low-priority-column" title="更新时间" :width="165">
            <template #cell="{ record }">{{ formatTime(record.updated_at) }}</template>
          </a-table-column>
          <a-table-column title="操作" :width="112" align="center" :fixed="'right'">
            <template #cell="{ record }">
              <a-space>
                <a-tooltip content="编辑实例"
                  ><a-button size="mini" type="text" :aria-label="`编辑 ${record.service_name}`" @click="openEdit(record)"
                    ><template #icon><icon-edit /></template></a-button
                ></a-tooltip>
                <a-popconfirm content="确认删除该服务部署信息？" @ok="remove(record)">
                  <a-tooltip content="删除实例"
                    ><a-button size="mini" type="text" status="danger" :aria-label="`删除 ${record.service_name}`"
                      ><template #icon><icon-delete /></template></a-button
                  ></a-tooltip>
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
        :body-style="{ maxHeight: 'calc(100vh - 176px)', overflowY: 'auto', padding: '18px var(--moox-space-6) 14px' }"
        :ok-loading="submitting"
        @before-ok="submit"
      >
        <a-form class="deployment-form" :model="form" layout="vertical">
          <a-form-item field="node_id" label="网关节点" required>
            <a-select v-model="form.node_id" :disabled="editing" placeholder="选择节点">
              <a-option v-for="node in nodes" :key="node.node_id" :value="node.node_id"
                >{{ node.name }} ({{ node.node_id }})</a-option
              >
            </a-select>
          </a-form-item>
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
            <a-select v-model="form.protocol">
              <a-option value="http">http</a-option>
              <a-option value="https">https</a-option>
              <a-option value="trpc">trpc</a-option>
            </a-select>
          </a-form-item>
          <a-form-item field="host" label="Host" required>
            <a-input v-model="form.host" placeholder="例如 106.53.107.122" />
          </a-form-item>
          <a-form-item field="port" label="端口" required>
            <a-input-number v-model="form.port" :min="1" :max="65535" />
          </a-form-item>
          <a-form-item field="gateway_enabled" label="Gateway 暴露">
            <a-switch v-model="form.gateway_enabled" />
          </a-form-item>
          <a-form-item field="gateway_service_id" label="Gateway service ID" :required="form.gateway_enabled">
            <a-input v-model="form.gateway_service_id" placeholder="例如 monitor" />
          </a-form-item>
          <a-form-item field="gateway_path" label="tRPC service path" :required="form.gateway_enabled">
            <a-input v-model="form.gateway_path" placeholder="trpc.moox.monitor.Monitor" />
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
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from "vue";
import { Message } from "@arco-design/web-vue";
import { reportControlError } from "@/api/admin/http";
import {
  createServiceDeployment,
  deleteServiceDeployment,
  listGatewayNodes,
  listServiceDeployments,
  updateServiceDeployment
} from "@/api/admin/sysdeploy";
import type { GatewayNode, ServiceDeployment, ServiceDeploymentInput } from "@/api/admin/types";
import { applyPageResult, defaultPagination, formatTime, statusColor } from "@/views/data/shared/metadata-utils";
import { createLatestRequestGuard, runModalSubmission, serviceDeploymentRowKey, validateGatewayDeployment } from "./health";

defineOptions({ name: "SettingsServiceDeployments" });
defineProps<{ embedded?: boolean }>();

const kindOptions = ["gateway", "frontend", "storage", "storage_rpc", "admin_rpc", "collector", "cloudnode", "trade"];
const rows = ref<ServiceDeployment[]>([]);
const nodes = ref<GatewayNode[]>([]);
const loading = ref(false);
const submitting = ref(false);
const visible = ref(false);
const editing = ref(false);
const editingServiceName = ref("");
const editingNodeID = ref("");
const pagination = reactive(defaultPagination());
const filters = reactive<{
  service_name: string;
  service_kind: string;
  scope: string;
  status: string;
  node_id: string;
  gateway_enabled?: boolean;
}>({
  service_name: "",
  service_kind: "",
  scope: "",
  status: "",
  node_id: "",
  gateway_enabled: undefined
});
const viewportHeight = ref(typeof window === "undefined" ? 900 : window.innerHeight);
const tableBodyHeight = computed(() => Math.max(320, viewportHeight.value - 440));
const loadGuard = createLatestRequestGuard();

const form = reactive<ServiceDeploymentInput>({
  service_name: "",
  service_kind: "service",
  protocol: "http",
  host: "",
  port: 0,
  gateway_path: "",
  scope: "public",
  status: "active",
  description: "",
  extra_config: "{}",
  node_id: "",
  gateway_service_id: "",
  gateway_enabled: false
});

const modalTitle = computed(() => (editing.value ? "编辑服务实例" : "新增服务实例"));

async function load() {
  const generation = loadGuard.begin();
  loading.value = true;
  try {
    const [rsp, nodesRsp] = await Promise.all([
      listServiceDeployments({
        service_name: filters.service_name || undefined,
        service_kind: filters.service_kind || undefined,
        scope: filters.scope || undefined,
        status: filters.status || undefined,
        node_id: filters.node_id || undefined,
        gateway_enabled: filters.gateway_enabled,
        page: { page: pagination.current, size: pagination.pageSize }
      }),
      listGatewayNodes({ page: { page: 1, size: 500 } })
    ]);
    if (!loadGuard.isCurrent(generation)) return;
    rows.value = rsp.deployments || [];
    nodes.value = nodesRsp.nodes || [];
    applyPageResult(pagination, rsp.page_result);
  } catch (error) {
    reportControlError(error);
    // The shared API client reports the error; keep the last valid snapshot visible.
  } finally {
    if (loadGuard.isCurrent(generation)) loading.value = false;
  }
}

function resetForm() {
  Object.assign(form, {
    service_name: "",
    service_kind: "service",
    protocol: "http",
    host: "",
    port: 0,
    gateway_path: "",
    scope: "public",
    status: "active",
    description: "",
    extra_config: "{}",
    node_id: filters.node_id || nodes.value[0]?.node_id || "",
    gateway_service_id: "",
    gateway_enabled: false
  });
}

function openCreate() {
  editing.value = false;
  editingServiceName.value = "";
  editingNodeID.value = "";
  resetForm();
  visible.value = true;
}

function openEdit(record: ServiceDeployment) {
  editing.value = true;
  editingServiceName.value = record.service_name;
  editingNodeID.value = record.node_id;
  Object.assign(form, deploymentInput(record));
  visible.value = true;
}

function deploymentInput(record: ServiceDeployment): ServiceDeploymentInput {
  return {
    service_name: record.service_name,
    service_kind: record.service_kind,
    protocol: record.protocol,
    host: record.host,
    port: record.port,
    gateway_path: record.gateway_path || "",
    scope: record.scope,
    status: record.status,
    description: record.description || "",
    extra_config: record.extra_config || "{}",
    node_id: record.node_id,
    gateway_service_id: record.gateway_service_id || "",
    gateway_enabled: record.gateway_enabled === true
  };
}

async function submit() {
  submitting.value = true;
  const result = await runModalSubmission(
    () => {
      const error =
        !form.node_id || !form.service_name || !form.host || !form.port
          ? "请补全网关节点、服务名、Host 和端口"
          : validateGatewayDeployment(form);
      if (error) Message.warning(error);
      return error;
    },
    async () => {
      const payload = { ...form, extra_config: form.extra_config || "{}" };
      if (editing.value) await updateServiceDeployment(editingNodeID.value, editingServiceName.value, payload);
      else await createServiceDeployment(payload);
      if (payload.service_name.startsWith("storage_")) Message.warning("服务部署已保存；storage 变更后请同步检查主存节点拓扑");
      else Message.success("服务部署信息已保存");
      await load();
    },
    reportControlError
  );
  submitting.value = false;
  return result;
}

async function remove(record: ServiceDeployment) {
  try {
    await deleteServiceDeployment(record.node_id, record.service_name);
    Message.success("服务部署信息已删除");
    await load();
  } catch (error) {
    reportControlError(error);
  }
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

function nodeLabel(nodeID: string) {
  const node = nodes.value.find(item => item.node_id === nodeID);
  return node ? node.name || node.node_id : nodeID || "--";
}

onMounted(() => {
  updateViewportHeight();
  window.addEventListener("resize", updateViewportHeight);
  load();
});

onUnmounted(() => {
  loadGuard.invalidate();
  window.removeEventListener("resize", updateViewportHeight);
});
</script>

<style scoped>
.moox-page {
  height: 100%;
  min-height: 0;
  overflow-y: auto;
}

.service-deployments-table {
  margin-bottom: var(--moox-space-5);
}

.service-deployments-table :deep(.arco-table-container) {
  max-width: 100%;
}

.instance-name,
.gateway-cell {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 3px;
}

.instance-name span,
.gateway-cell span {
  overflow: hidden;
  color: var(--color-text-3);
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.gateway-cell strong {
  overflow: hidden;
  font-weight: 500;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.service-deployments-table code {
  font-size: 12px;
}

.deployment-form {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  column-gap: var(--moox-space-4);
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
  margin-bottom: var(--moox-space-1);
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

  .service-deployments-table :deep(th:nth-child(1)),
  .service-deployments-table :deep(td:nth-child(1)),
  .service-deployments-table :deep(th:nth-child(3)),
  .service-deployments-table :deep(td:nth-child(3)),
  .service-deployments-table :deep(th:nth-child(5)),
  .service-deployments-table :deep(td:nth-child(5)),
  .service-deployments-table :deep(th:nth-child(6)),
  .service-deployments-table :deep(td:nth-child(6)) {
    display: none;
  }
}

@media (max-width: 560px) {
  .service-deployments-table :deep(th:nth-child(4)),
  .service-deployments-table :deep(td:nth-child(4)) {
    display: none;
  }
}

.filters {
  margin-bottom: var(--moox-space-2);
}
</style>
