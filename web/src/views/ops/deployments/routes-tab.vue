<template>
  <div class="routes-tab">
    <div class="toolbar">
      <a-select
        v-model="hostId"
        class="toolbar__host"
        placeholder="选择主机"
        aria-label="主机"
        :loading="hostsLoading"
        @change="loadRoutes"
      >
        <a-option v-for="host in hosts" :key="host.host_id" :value="host.host_id">
          {{ host.host_id }}<template v-if="host.status === 'disabled'">（已停用）</template>
        </a-option>
      </a-select>
      <a-select v-model="caller" class="toolbar__caller" placeholder="调用方" allow-clear aria-label="调用方">
        <a-option v-for="item in callers" :key="item" :value="item">{{ item }}</a-option>
      </a-select>
      <a-input-search v-model="method" class="toolbar__method" placeholder="方法名" allow-clear aria-label="方法名" />
      <a-button size="small" :loading="routesLoading" aria-label="刷新" @click="loadRoutes">刷新</a-button>
      <InfoTip
        text="主机网关的路由由 Admin 按组件目录和启用的部署生成，这里展示 Admin 期望的快照；主机网关应用后，同步状态变为已同步。"
      />
    </div>

    <a-alert v-if="error" type="error" class="tab-alert">{{ error }}</a-alert>
    <a-alert v-if="snapshot?.disabled" type="warning" class="tab-alert">
      主机 {{ snapshot.host_id }} 已停用，主机网关不承接这台主机上的路由。
    </a-alert>

    <div v-if="snapshot" class="snapshot-head" role="status">
      <span
        >快照版本 <code>{{ shortHash(snapshot.expected_hash) }}</code></span
      >
      <span>生成时间 {{ formatTime(snapshot.generated_at) }}</span>
      <span>
        同步状态
        <a-tag size="small" :color="snapshot.gateway?.synced ? 'green' : 'orange'">{{ syncText(snapshot.gateway) }}</a-tag>
      </span>
      <span>主机网关 {{ gatewayStateText(snapshot.gateway?.state) }}</span>
      <span>
        已应用版本 <code>{{ shortHash(snapshot.gateway?.applied_hash) }}</code>
      </span>
      <span>最近心跳 {{ formatTime(snapshot.gateway?.last_seen_at) }}</span>
      <span v-if="snapshot.gateway?.instance_id">
        实例 <code>{{ shortHash(snapshot.gateway.instance_id) }}</code>
        <template v-if="snapshot.gateway.version">（{{ snapshot.gateway.version }}）</template>
      </span>
      <span v-if="snapshot.gateway?.state === 'conflict'" class="snapshot-head__error">
        另有实例 <code>{{ shortHash(snapshot.gateway.conflict_instance_id) }}</code> 在
        {{ formatTime(snapshot.gateway.conflict_seen_at) }} 也在上报，这台主机上可能启动了两个主机网关
      </span>
      <span>{{ visibleRoutes.length }} / {{ routes.length }} 条路由</span>
    </div>
    <a-alert v-if="snapshot?.gateway?.last_error" type="warning" class="tab-alert">
      主机网关最近一次错误：{{ snapshot.gateway.last_error }}
    </a-alert>

    <a-table
      :data="visibleRoutes"
      :loading="routesLoading"
      :pagination="false"
      size="small"
      :row-key="routeKey"
      :bordered="{ cell: true }"
      :scroll="{ x: 'max-content' }"
      :expandable="{ width: 36 }"
    >
      <template #columns>
        <a-table-column title="tRPC 服务" :width="300">
          <template #cell="{ record }">
            <div>{{ record.service_path }}</div>
            <div class="muted">{{ record.component_id }}</div>
          </template>
        </a-table-column>
        <a-table-column title="上游端口" :width="90">
          <template #cell="{ record }">{{ upstreamPort(record.address) ?? "—" }}</template>
        </a-table-column>
        <a-table-column title="方法" :width="80">
          <template #cell="{ record }">{{ (record.methods || []).length }}</template>
        </a-table-column>
        <a-table-column title="调用方" :width="360">
          <template #cell="{ record }">
            <a-tag v-for="item in record.callers || []" :key="item" size="small" class="caller-tag">{{ item }}</a-tag>
          </template>
        </a-table-column>
        <a-table-column title="超时" :width="80">
          <template #cell="{ record }">{{ formatTimeout(record.timeout_ms) }}</template>
        </a-table-column>
        <a-table-column title="包体上限" :width="100">
          <template #cell="{ record }">{{ formatBytes(record.max_body_bytes) }}</template>
        </a-table-column>
      </template>
      <template #expand-row="{ record }">
        <div class="method-list">
          <code v-for="name in record.methods || []" :key="name">{{ name }}</code>
        </div>
      </template>
    </a-table>
  </div>
</template>

<script setup lang="ts">
import { computed, onActivated, onMounted, ref } from "vue";
import { sysdeployApi } from "@/api/admin/sysdeploy";
import type { DeployHost, HostRoute, HostRoutes } from "@/api/admin/types";
import { createLatestRequestGuard } from "@/utils/latest-request";
import InfoTip from "@/views/factor/shared/info-tip.vue";
import { formatTime } from "@/views/ops/monitor/monitor-display";
import {
  filterRoutes,
  formatBytes,
  formatTimeout,
  gatewayStateText,
  routeCallers,
  syncText,
  upstreamPort
} from "./deployment-view";

const props = defineProps<{ initialHost?: string }>();

const hosts = ref<DeployHost[]>([]);
const hostId = ref(props.initialHost || "");
const caller = ref("");
const method = ref("");
const snapshot = ref<HostRoutes | null>(null);
const hostsLoading = ref(false);
const routesLoading = ref(false);
const error = ref("");
const routesGuard = createLatestRequestGuard();

const routes = computed(() => snapshot.value?.routes || []);
const callers = computed(() => routeCallers(routes.value));
const visibleRoutes = computed(() => filterRoutes(routes.value, caller.value || "", method.value));

function routeKey(route: HostRoute) {
  return `${route.component_id}:${route.service_path}`;
}

function shortHash(hash?: string) {
  if (!hash) return "—";
  return hash.replace(/^sha256:/, "").slice(0, 12);
}

async function loadRoutes() {
  if (!hostId.value) return;
  const request = routesGuard.begin();
  routesLoading.value = true;
  error.value = "";
  try {
    const rsp = await sysdeployApi.getHostRoutes(hostId.value);
    if (!request.isLatest()) return;
    snapshot.value = rsp;
    if (caller.value && !routeCallers(rsp.routes || []).includes(caller.value)) caller.value = "";
  } catch (err) {
    if (request.isLatest()) {
      snapshot.value = null;
      error.value = err instanceof Error ? err.message : "路由加载失败";
    }
  } finally {
    if (request.isLatest()) routesLoading.value = false;
  }
}

async function loadHosts() {
  hostsLoading.value = true;
  try {
    const rsp = await sysdeployApi.listHosts();
    hosts.value = (rsp.hosts || []).slice().sort((left, right) => {
      if (left.host_id === "control") return -1;
      if (right.host_id === "control") return 1;
      return (left.host_id || "").localeCompare(right.host_id || "");
    });
    if (!hosts.value.some(host => host.host_id === hostId.value)) hostId.value = hosts.value[0]?.host_id || "";
    await loadRoutes();
  } catch (err) {
    error.value = err instanceof Error ? err.message : "主机加载失败";
  } finally {
    hostsLoading.value = false;
  }
}

onMounted(() => void loadHosts());
// 标签页被缓存：在「服务」页启停部署后切回来，路由和同步状态要重新取。
let activatedOnce = false;
onActivated(() => {
  if (!activatedOnce) {
    activatedOnce = true;
    return;
  }
  void loadRoutes();
});
</script>

<style scoped lang="scss">
.toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--moox-space-2);
  margin-bottom: var(--moox-space-2);
}

.toolbar__host {
  width: 200px;
}

.toolbar__caller {
  width: 180px;
}

.toolbar__method {
  width: 220px;
}

.snapshot-head__error {
  color: rgb(var(--danger-6));
}

.tab-alert {
  margin-bottom: var(--moox-space-2);
}

.snapshot-head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--moox-space-2) var(--moox-space-4);
  margin-bottom: var(--moox-space-2);
  color: var(--color-text-2);
}

.muted {
  color: var(--color-text-3);
}

.caller-tag {
  margin: 2px 4px 2px 0;
}

.method-list {
  display: flex;
  flex-wrap: wrap;
  gap: var(--moox-space-1) var(--moox-space-3);
}
</style>
