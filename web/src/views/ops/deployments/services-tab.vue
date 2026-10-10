<template>
  <div class="services-tab">
    <div class="toolbar">
      <a-input-search
        v-model="search"
        class="toolbar__search"
        placeholder="搜索组件、主机或服务"
        allow-clear
        aria-label="搜索组件、主机或服务"
      />
      <a-select v-model="filter" class="toolbar__filter" aria-label="状态筛选">
        <a-option value="all">全部</a-option>
        <a-option value="attention">异常</a-option>
        <a-option value="disabled">已停用</a-option>
      </a-select>
      <label class="toolbar__switch">
        <a-switch v-model="showDisabled" size="small" :disabled="filter === 'disabled'" />
        显示已停用
      </label>
      <InfoTip
        text="主机和部署来自 moox.toml 的部署表，由 moox-cli 部署时同步。这里只能启用或停用：停用只摘掉路由、停止健康检查，不会停止进程。"
      />
      <span class="toolbar__summary">
        {{ summary.hosts }} 台主机 · {{ summary.deployments }} 个部署 · {{ summary.attention }} 个需关注
      </span>
      <a-button size="small" :loading="loading" class="toolbar__refresh" @click="refresh">
        <template #icon><icon-refresh /></template>
        刷新
      </a-button>
    </div>

    <a-alert v-if="error" type="error" class="tab-alert">{{ error }}</a-alert>
    <a-alert v-if="healthError" type="warning" class="tab-alert">{{ healthError }}</a-alert>

    <a-empty v-if="loaded && !visibleGroups.length" description="没有符合条件的部署" />

    <section
      v-for="group in visibleGroups"
      :key="group.hostId"
      class="host-group"
      :class="{ 'host-group--disabled': !group.enabled, 'host-group--attention': group.attention }"
      :aria-label="`主机 ${group.hostId}`"
    >
      <header class="host-head">
        <div class="host-head__main">
          <strong class="host-head__id">{{ group.hostId }}</strong>
          <span class="muted">{{ hostAddress(group) }}</span>
          <a-tag v-if="!group.enabled" color="gray" size="small">已停用</a-tag>
          <a-tag v-if="group.host.protected" size="small">受保护</a-tag>
        </div>
        <div class="host-head__status">
          <span>
            主机网关
            <a-tag size="small" :color="gatewayStateColor(group.gatewayState)">{{ gatewayStateText(group.gatewayState) }}</a-tag>
          </span>
          <span>路由 {{ syncText(group.host.gateway) }}</span>
          <span v-for="row in group.hostRows" :key="row.key" class="host-component">
            {{ row.name }}
            <a-tag size="small" :color="statusColor(row.healthStatus)">{{ statusLabel(row.healthStatus) }}</a-tag>
          </span>
        </div>
        <div v-if="!group.host.protected" class="host-head__actions">
          <a-popconfirm
            :content="group.enabled ? hostDisableConfirm(group.hostId) : `启用主机 ${group.hostId}？`"
            @ok="toggleHost(group)"
          >
            <a-button size="mini" :status="group.enabled ? 'warning' : 'normal'" :loading="pendingHost === group.hostId">
              {{ group.enabled ? "停用主机" : "启用主机" }}
            </a-button>
          </a-popconfirm>
        </div>
      </header>

      <a-table
        v-if="group.rows.length"
        :data="group.rows"
        :pagination="false"
        size="small"
        row-key="key"
        :bordered="{ cell: true }"
        :scroll="{ x: 'max-content' }"
        :row-class="rowClass"
      >
        <template #columns>
          <a-table-column title="组件" :width="220">
            <template #cell="{ record }">
              <a-link class="component-link" @click="emit('open', record)">{{ record.name }}</a-link>
              <div class="muted">{{ record.componentId }}</div>
            </template>
          </a-table-column>
          <a-table-column title="健康" :width="300">
            <template #cell="{ record }">
              <a-tag size="small" :color="statusColor(record.healthStatus)">{{ statusLabel(record.healthStatus) }}</a-tag>
              <span class="row-reason">{{ record.healthReason }}</span>
            </template>
          </a-table-column>
          <a-table-column title="tRPC 服务" :width="320">
            <template #cell="{ record }">
              <div v-for="service in record.services" :key="service.path" class="service-line">
                {{ service.path }}<span class="muted"> :{{ service.port }}</span>
              </div>
              <span v-if="!record.services.length" class="muted">—</span>
            </template>
          </a-table-column>
          <a-table-column title="上报实例" :width="220">
            <template #cell="{ record }">
              <template v-if="record.health?.reporter?.instance_id">
                <div>{{ record.health.reporter.instance_id }}</div>
                <div class="muted">{{ record.health.reporter.version || "—" }}</div>
              </template>
              <span v-else class="muted">{{ reporterText(record) }}</span>
            </template>
          </a-table-column>
          <a-table-column title="启用" :width="90" fixed="right">
            <template #cell="{ record }">
              <a-tag v-if="record.protected" size="small">受保护</a-tag>
              <a-switch
                v-else
                size="small"
                :model-value="record.enabled"
                :disabled="!group.enabled"
                :before-change="(value: string | number | boolean) => togglePlacement(record, Boolean(value))"
                :aria-label="`启用 ${record.componentId}@${record.hostId}`"
              />
            </template>
          </a-table-column>
        </template>
      </a-table>

      <div v-if="group.unregistered.length" class="unregistered">
        <a-alert type="warning">
          这台主机上有 {{ group.unregistered.length }} 个未登记的进程在上报运行指标，Monitor 不接收它们的上报。请在 moox.toml
          的部署表中登记，或停掉这些进程：
          <span v-for="item in group.unregistered" :key="item.instance_id" class="unregistered__item">
            {{ item.instance_id }}<template v-if="item.version">（{{ item.version }}）</template>
          </span>
        </a-alert>
      </div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed, onActivated, onDeactivated, onMounted, onUnmounted, ref } from "vue";
import { Message, Modal } from "@arco-design/web-vue";
import { IconRefresh } from "@arco-design/web-vue/es/icon";
import { sysdeployApi } from "@/api/admin/sysdeploy";
import type { CatalogComponent, DeployHost, DeployPlacement } from "@/api/admin/types";
import { monitorApi, type HealthComponent, type HealthUnregistered } from "@/api/monitor";
import { createLatestRequestGuard } from "@/utils/latest-request";
import InfoTip from "@/views/factor/shared/info-tip.vue";
import { statusColor, statusLabel } from "@/views/ops/monitor/monitor-display";
import {
  buildHostGroups,
  filterHostGroups,
  gatewayStateColor,
  gatewayStateText,
  summarizeGroups,
  syncText,
  type DeploymentRow,
  type HostGroup,
  type ServiceFilter
} from "./deployment-view";

const props = defineProps<{ focus?: string }>();
const emit = defineEmits<{ open: [row: DeploymentRow]; loaded: [rows: DeploymentRow[]] }>();

const POLL_INTERVAL_MS = 30_000;
const search = ref("");
const filter = ref<ServiceFilter>("all");
const showDisabled = ref(true);
const loading = ref(false);
const loaded = ref(false);
const error = ref("");
const healthError = ref("");
const pendingHost = ref("");
const hosts = ref<DeployHost[]>([]);
const placements = ref<DeployPlacement[]>([]);
const catalog = ref<CatalogComponent[]>([]);
const health = ref<HealthComponent[]>([]);
const unregistered = ref<HealthUnregistered[]>([]);
const refreshGuard = createLatestRequestGuard();
let timer: number | undefined;

const groups = computed(() =>
  buildHostGroups({
    hosts: hosts.value,
    placements: placements.value,
    catalog: catalog.value,
    health: health.value,
    unregistered: unregistered.value
  })
);
const visibleGroups = computed(() =>
  filterHostGroups(groups.value, { search: search.value, filter: filter.value, showDisabled: showDisabled.value })
);
const summary = computed(() => summarizeGroups(groups.value));

function hostAddress(group: HostGroup) {
  const parts = [group.host.address];
  if (group.host.private_address) parts.push(`私网 ${group.host.private_address}`);
  if (group.host.region) parts.push(group.host.region);
  return parts.filter(Boolean).join(" · ");
}

function hostDisableConfirm(hostId: string) {
  return `停用主机 ${hostId}：摘掉这台主机上全部组件的路由、停止健康检查，进程不会停止。确定停用？`;
}

function reporterText(row: DeploymentRow) {
  switch (row.health?.reporter?.status) {
    case "never_reported":
      return "从未上报";
    case "stale":
      return "上报中断";
    default:
      return "—";
  }
}

function rowClass(record: DeploymentRow) {
  const classes = [];
  if (!record.enabled) classes.push("row--disabled");
  if (props.focus === record.key) classes.push("row--focus");
  return classes.join(" ");
}

async function togglePlacement(row: DeploymentRow, enabled: boolean) {
  if (!enabled) {
    const confirmed = await new Promise<boolean>(resolve => {
      Modal.confirm({
        title: `停用 ${row.name}@${row.hostId}`,
        content: "停用后主机网关摘掉它的路由，Monitor 停止它的健康检查；进程不会停止。",
        okText: "停用",
        onOk: () => resolve(true),
        onCancel: () => resolve(false)
      });
    });
    if (!confirmed) return false;
  }
  try {
    await sysdeployApi.setPlacementStatus(row.hostId, row.componentId, enabled ? "enabled" : "disabled");
    Message.success(`${row.componentId}@${row.hostId} 已${enabled ? "启用" : "停用"}`);
    void refresh();
    return true;
  } catch (err) {
    Message.error(err instanceof Error ? err.message : "操作失败");
    return false;
  }
}

async function toggleHost(group: HostGroup) {
  pendingHost.value = group.hostId;
  try {
    await sysdeployApi.setHostStatus(group.hostId, group.enabled ? "disabled" : "enabled");
    Message.success(`主机 ${group.hostId} 已${group.enabled ? "停用" : "启用"}`);
    await refresh();
  } catch (err) {
    Message.error(err instanceof Error ? err.message : "操作失败");
  } finally {
    pendingHost.value = "";
  }
}

async function refresh() {
  const request = refreshGuard.begin();
  loading.value = true;
  error.value = "";
  try {
    const [hostRsp, placementRsp, catalogRsp] = await Promise.all([
      sysdeployApi.listHosts(),
      sysdeployApi.listPlacements(),
      catalog.value.length ? Promise.resolve({ components: catalog.value }) : sysdeployApi.getCatalog()
    ]);
    if (!request.isLatest()) return;
    hosts.value = hostRsp.hosts || [];
    placements.value = placementRsp.placements || [];
    catalog.value = catalogRsp.components || [];
    loaded.value = true;
  } catch (err) {
    if (request.isLatest()) error.value = err instanceof Error ? err.message : "部署记录加载失败";
  }
  // 健康状态来自 Monitor；Monitor 不可用时仍然显示部署记录。
  try {
    const rsp = await monitorApi.getOverview();
    if (!request.isLatest()) return;
    health.value = rsp.overview?.components || [];
    unregistered.value = rsp.overview?.unregistered || [];
    healthError.value = "";
  } catch (err) {
    if (request.isLatest()) healthError.value = `健康状态暂时不可用：${err instanceof Error ? err.message : "Monitor 无响应"}`;
  } finally {
    if (request.isLatest()) {
      loading.value = false;
      emit(
        "loaded",
        groups.value.flatMap(group => [...group.hostRows, ...group.rows])
      );
    }
  }
}

function startPolling() {
  if (timer) return;
  timer = window.setInterval(() => void refresh(), POLL_INTERVAL_MS);
}

function stopPolling() {
  if (timer) window.clearInterval(timer);
  timer = undefined;
}

onMounted(() => {
  void refresh();
  startPolling();
});
onActivated(() => {
  if (timer) return;
  void refresh();
  startPolling();
});
onDeactivated(stopPolling);
onUnmounted(stopPolling);

defineExpose({ refresh });
</script>

<style scoped lang="scss">
.toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--moox-space-2);
  margin-bottom: var(--moox-space-2);
}

.toolbar__search {
  width: 260px;
}

.toolbar__filter {
  width: 110px;
}

.toolbar__switch {
  display: inline-flex;
  align-items: center;
  gap: var(--moox-space-1);
  color: var(--color-text-2);
}

.toolbar__summary {
  color: var(--color-text-3);
}

.toolbar__refresh {
  margin-left: auto;
}

.tab-alert {
  margin-bottom: var(--moox-space-2);
}

.host-group {
  margin-top: var(--moox-space-3);
  padding: var(--moox-space-3);
  border: 1px solid var(--color-border-2);
  border-radius: 6px;
  background: var(--color-bg-2);
}

.host-group--attention {
  border-left: 3px solid rgb(var(--orange-6));
}

.host-group--disabled {
  opacity: 0.75;
}

.host-head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--moox-space-2) var(--moox-space-4);
  margin-bottom: var(--moox-space-2);
}

.host-head__main,
.host-head__status {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--moox-space-2);
}

.host-head__id {
  font-size: 15px;
}

.host-head__status {
  color: var(--color-text-2);
}

.host-head__actions {
  margin-left: auto;
}

.host-component {
  display: inline-flex;
  align-items: center;
  gap: var(--moox-space-1);
}

.muted {
  color: var(--color-text-3);
}

.component-link {
  padding: 0;
  font-weight: 500;
}

.row-reason {
  margin-left: var(--moox-space-1);
  color: var(--color-text-2);
}

.service-line {
  white-space: nowrap;
}

.unregistered {
  margin-top: var(--moox-space-2);
}

.unregistered__item {
  margin-left: var(--moox-space-2);
  font-family: var(--moox-font-mono, monospace);
}

:deep(.row--disabled) td {
  color: var(--color-text-3);
}

:deep(.row--focus) td {
  background: rgb(var(--arcoblue-1));
}
</style>
