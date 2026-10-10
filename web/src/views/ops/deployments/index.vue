<template>
  <div class="moox-page deployments-page">
    <div class="moox-inner">
      <div class="page-head">
        <PageTitleTabs v-model="tab" :items="tabs" aria-label="服务部署" />
        <a-button :loading="loading" :disabled="!!busy" aria-label="刷新部署状态" @click="refresh">刷新</a-button>
      </div>
      <p class="muted">主机与组件由部署清单登记。停用会撤下路由和健康检查，进程仍由部署工具管理。</p>
      <a-alert v-if="error" type="error">{{ error }}。以下保留上次数据，健康状态未知，暂不可修改。</a-alert>
      <a-alert v-if="monitorError" type="warning">{{ monitorError }}。组件健康状态暂不可确认。</a-alert>
      <a-alert v-if="locatorError" type="warning">{{ locatorError }}</a-alert>
      <div v-if="requestedHost || requestedComponent" class="locator-filter">
        定位：{{ requestedHost }} {{ requestedComponent }} <a-button type="text" @click="clearLocator">清除定位</a-button>
      </div>
      <div class="deployments-content">
        <template v-if="tab === 'services'">
          <div class="filters">
            <a-input v-model="search" allow-clear placeholder="搜索主机、组件名称或 ID" aria-label="搜索部署" />
            <a-select v-model="filter" aria-label="部署状态筛选">
              <a-option value="all">全部</a-option><a-option value="abnormal">异常</a-option
              ><a-option value="disabled">已停用</a-option>
            </a-select>
            <a-checkbox v-model="showDisabled">显示已停用</a-checkbox>
          </div>
          <section v-for="group in visibleGroups" :key="group.host.host_id" class="host-group" :data-host-id="group.host.host_id">
            <div class="host-heading">
              <div>
                <h3>
                  {{ group.host.host_id }} <a-tag v-if="group.host.host_id === controlHostId">控制主机 · 受保护</a-tag
                  ><a-tag v-if="group.host.status === 'disabled'">已停用</a-tag>
                </h3>
                <p>
                  {{ group.host.address }}
                  <span v-if="group.host.private_address">· 内网 {{ group.host.private_address }}</span> ·
                  {{ group.host.region || "未指定地域" }}
                </p>
                <p v-if="group.host.description" class="muted">{{ group.host.description }}</p>
              </div>
              <button
                v-if="group.host.host_id !== controlHostId"
                type="button"
                class="status-toggle"
                role="switch"
                :aria-checked="group.host.status === 'enabled'"
                :aria-label="`主机 ${group.host.host_id} 启用状态`"
                :disabled="!!busy || !!error"
                @click="toggleHost(group.host)"
              >
                {{ group.host.status === "enabled" ? "停用主机" : "启用主机" }}
              </button>
            </div>
            <p class="gateway-summary">
              主机网关：<strong>{{ error ? "状态未知" : gatewayLabel(routes[group.host.host_id]) }}</strong> · 路由：{{
                syncFor(group.host.host_id).label
              }}
              <span v-if="syncFor(group.host.host_id).pendingSince"
                >（自 {{ formatCheckedAt(syncFor(group.host.host_id).pendingSince) }}）</span
              >
              <span v-else-if="syncFor(group.host.host_id).label === '待同步'">（起点尚未观测）</span>
              · 组件：{{
                group.rows
                  .map(row => `${row.definition?.name || row.placement.component_id} ${statusLabel(row.status)}`)
                  .join(" / ")
              }}
            </p>
            <p v-if="routeErrors[group.host.host_id]" class="error" role="alert">
              路由读取失败：{{ routeErrors[group.host.host_id] }}
            </p>
            <p class="hashes">
              期望 {{ routes[group.host.host_id]?.gateway_status?.expected_hash || "暂无" }}<br />已应用
              {{ routes[group.host.host_id]?.gateway_status?.applied_hash || "暂无" }}
            </p>
            <div class="table-scroll">
              <table>
                <thead>
                  <tr>
                    <th>组件</th>
                    <th>健康状态</th>
                    <th>tRPC 服务 / 端口</th>
                    <th>上报实例 / 版本</th>
                    <th>部署状态</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="row in group.rows" :key="row.key" :data-component-id="row.placement.component_id">
                    <td>
                      <button type="button" class="detail-link" @click="drawerKey = row.key">
                        {{ row.definition?.name || row.placement.component_id }}</button
                      ><small>{{ row.placement.component_id }}</small>
                    </td>
                    <td>
                      <a-tag :color="statusColor(row.status)">{{ statusLabel(row.status) }}</a-tag
                      ><small>{{ row.health?.reason || (row.status === "disabled" ? "部署已停用" : "暂无健康观测") }}</small>
                    </td>
                    <td>
                      <div v-for="service in row.definition?.services || []" :key="service.path">
                        {{ service.path }} :{{ service.port }}
                      </div>
                      <small>端口：{{ componentPorts(row.definition).join(", ") || "无监听端口" }}</small>
                    </td>
                    <td>
                      <div v-for="instance in row.health?.instances || []" :key="`${instance.instance_id}/${instance.boot_id}`">
                        <span>{{ instance.instance_id }} · {{ instance.version || "版本未知" }}</span
                        ><small>{{ statusLabel(instance.status) }} · {{ formatCheckedAt(instance.last_reported_at) }}</small>
                      </div>
                      <span v-if="!row.health?.instances?.length" class="muted">暂无上报</span>
                    </td>
                    <td>
                      <a-tag v-if="row.definition?.protected">受保护</a-tag><span v-else-if="!row.definition">目录中不存在</span
                      ><button
                        v-else
                        type="button"
                        class="status-toggle"
                        role="switch"
                        :aria-checked="row.placement.status === 'enabled'"
                        :aria-label="`${group.host.host_id} ${row.placement.component_id} 启用状态`"
                        :disabled="!!busy || !!error || !canTogglePlacement(group.host, row)"
                        @click="togglePlacement(group.host, row)"
                      >
                        {{ row.placement.status === "enabled" ? "停用" : "启用" }}</button
                      ><small v-if="group.host.status === 'disabled'">主机已停用</small>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
          </section>
          <p v-if="loaded && !visibleGroups.length" class="empty">没有符合筛选条件的部署</p>
          <section v-if="unregistered.length" class="host-group unregistered">
            <h3>未登记的进程</h3>
            <p>请在 moox.toml 的部署表中登记以下进程，并由 CLI 同步清单。</p>
            <p v-for="item in unregistered" :key="`${item.host_id}/${item.component_id}`">
              {{ item.host_id }} · {{ item.component_id }} · {{ item.reason }}
            </p>
          </section>
        </template>
        <template v-else>
          <div class="filters">
            <a-select
              :model-value="selectedHostId"
              @update:model-value="selectRouteHost"
              placeholder="选择主机"
              aria-label="路由主机"
              ><a-option v-for="host in hosts" :key="host.host_id" :value="host.host_id"
                >{{ host.host_id }} · {{ host.address }}</a-option
              ></a-select
            >
            <a-select v-model="caller" allow-clear placeholder="调用方" aria-label="调用方筛选"
              ><a-option v-for="item in callers" :key="item" :value="item">{{ item }}</a-option></a-select
            >
            <a-input v-model="method" allow-clear placeholder="搜索方法" aria-label="方法筛选" />
          </div>
          <div v-if="currentRoutes" class="snapshot-summary">
            <p>
              快照格式 v{{ currentRoutes.snapshot_schema_version }} · 本次编译 {{ formatCheckedAt(currentRoutes.compiled_at) }} ·
              {{ syncFor(selectedHostId).label
              }}<span v-if="syncFor(selectedHostId).pendingSince"
                >（自 {{ formatCheckedAt(syncFor(selectedHostId).pendingSince) }}）</span
              >
            </p>
            <p class="hashes">
              路由定义 {{ currentRoutes.definition_hash }}<br />期望快照 {{ currentRoutes.gateway_status?.expected_hash || "暂无"
              }}<br />已应用快照 {{ currentRoutes.gateway_status?.applied_hash || "暂无" }}
            </p>
            <p v-if="currentRoutes.gateway_status?.last_error" class="error">{{ currentRoutes.gateway_status.last_error }}</p>
          </div>
          <a-alert v-if="currentRouteError" type="error">路由读取失败：{{ currentRouteError }}</a-alert>
          <div v-else-if="currentRoutes" class="table-scroll">
            <table class="route-table">
              <thead>
                <tr>
                  <th>tRPC 服务</th>
                  <th>上游端口</th>
                  <th>方法</th>
                  <th>调用方</th>
                  <th>超时</th>
                  <th>包体上限</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="group in routeGroups" :key="group.key">
                  <td>
                    {{ group.service }}<small>{{ group.componentId }}</small>
                  </td>
                  <td :title="group.address">{{ group.port }}</td>
                  <td>
                    <details>
                      <summary>{{ group.methods.length }} 个方法</summary>
                      <div v-for="item in group.methods" :key="item.method" class="method-detail">
                        <strong>{{ item.method }}</strong
                        ><small>调用方：{{ item.callers?.join(", ") || "无" }} · {{ item.read_only ? "只读" : "读写" }}</small>
                      </div>
                    </details>
                  </td>
                  <td>{{ group.callers.join(", ") || "无" }}</td>
                  <td>{{ group.timeout }} ms</td>
                  <td>{{ group.maxBody }} bytes</td>
                </tr>
              </tbody>
            </table>
            <p v-if="!routeGroups.length" class="empty">
              {{ selectedGroup?.host.status === "disabled" ? "主机已停用，快照没有可用路由" : "没有符合筛选条件的路由" }}
            </p>
          </div>
          <p v-else-if="loaded && !currentRouteError" class="empty">请选择已登记的主机</p>
        </template>
      </div>
      <small v-if="catalog" class="muted"
        >组件目录 v{{ catalog.version }} · {{ catalogHash }} · Monitor {{ formatCheckedAt(overview?.generated_at) }}</small
      >
      <a-drawer v-model:visible="drawerVisible" :width="640" title="组件详情" :footer="false">
        <template v-if="detail">
          <h3>{{ detail.row.definition?.name || detail.row.placement.component_id }}</h3>
          <dl class="component-detail">
            <dt>组件 ID</dt>
            <dd>{{ detail.row.placement.component_id }}</dd>
            <dt>二进制</dt>
            <dd>{{ detail.row.definition?.binary || "目录中不存在" }}</dd>
            <dt>部署主机</dt>
            <dd>{{ detail.host.host_id }} · {{ detail.host.address }}</dd>
            <dt>部署状态</dt>
            <dd>{{ detail.host.status === "disabled" || detail.row.placement.status === "disabled" ? "已停用" : "已启用" }}</dd>
            <dt>范围 / 副本</dt>
            <dd>{{ detail.row.definition?.scope }} / {{ detail.row.definition?.replicas }}</dd>
            <dt>端口</dt>
            <dd>{{ componentPorts(detail.row.definition).join(", ") || "无监听端口" }}</dd>
            <dt>健康检查</dt>
            <dd>
              {{ detail.row.definition?.health.kind
              }}<span v-if="detail.row.definition?.health.port"> :{{ detail.row.definition.health.port }}</span>
              {{ detail.row.health?.probe_url }}
            </dd>
            <dt>当前健康</dt>
            <dd>{{ statusLabel(detail.row.status) }} · {{ detail.row.health?.reason || "暂无健康观测" }}</dd>
            <dt>主动探测 / Reporter</dt>
            <dd>{{ statusLabel(detail.row.health?.probe?.status) }} / {{ statusLabel(detail.row.health?.reporter?.status) }}</dd>
            <dt>登记 / 更新</dt>
            <dd>
              {{ formatCheckedAt(detail.row.placement.created_at) }} / {{ formatCheckedAt(detail.row.placement.updated_at) }}
            </dd>
          </dl>
          <section v-if="detail.row.health?.probe?.raw_error">
            <h4>主动探测</h4>
            <RawError :value="detail.row.health.probe.raw_error" />
          </section>
          <section v-if="detail.row.health?.reporter?.raw_error">
            <h4>Reporter</h4>
            <RawError :value="detail.row.health.reporter.raw_error" />
          </section>
          <section v-for="service in detail.row.definition?.services || []" :key="service.path" class="service-detail">
            <h4>{{ service.path }} :{{ service.port }}</h4>
            <p>方法：{{ service.methods.join(", ") }}</p>
            <p v-for="(grant, index) in service.acl" :key="index">
              {{ grant.methods.join(", ") }} → {{ grant.callers.join(", ") }}
            </p>
          </section>
          <h4>上报实例</h4>
          <p v-for="instance in detail.row.health?.instances || []" :key="`${instance.instance_id}/${instance.boot_id}`">
            {{ instance.instance_id }} · {{ instance.boot_id }} · {{ instance.version || "版本未知" }} ·
            {{ statusLabel(instance.status) }} · {{ formatCheckedAt(instance.last_reported_at) }}
          </p>
          <p v-if="!detail.row.health?.instances?.length" class="muted">暂无上报</p>
        </template>
      </a-drawer>
    </div>
  </div>
</template>
<script setup lang="ts">
import { computed, ref, watch } from "vue";
import PageTitleTabs from "@/components/page-title-tabs/index.vue";
import RawError from "@/views/ops/monitor/raw-error.vue";
import { formatCheckedAt, statusColor, statusLabel } from "@/views/ops/monitor/health-display";
import { canTogglePlacement, componentPorts, gatewayLabel, groupRoutes, routeSync } from "./model";
import { useDeployments } from "./use-deployments";
defineOptions({ name: "OpsDeployments" });
const tabs = [
  { key: "services", label: "服务" },
  { key: "routes", label: "网关路由" }
];
const {
  tab,
  catalog,
  catalogHash,
  controlHostId,
  hosts,
  overview,
  unregistered,
  loading,
  loaded,
  error,
  monitorError,
  busy,
  search,
  filter,
  showDisabled,
  selectedHostId,
  visibleGroups,
  selectedGroup,
  currentRoutes,
  currentRouteError,
  routes,
  routeErrors,
  drawerKey,
  drawerVisible,
  detail,
  locatorError,
  requestedHost,
  requestedComponent,
  clearLocator,
  selectRouteHost,
  gatewaySignal,
  refresh,
  toggleHost,
  togglePlacement
} = useDeployments();
const caller = ref("");
const method = ref("");
watch(selectedHostId, () => {
  caller.value = "";
  method.value = "";
});
const callers = computed(() => [...new Set((currentRoutes.value?.routes || []).flatMap(item => item.callers || []))].sort());
const routeGroups = computed(() => groupRoutes(currentRoutes.value?.routes || [], caller.value || "", method.value));
function syncFor(hostId: string) {
  return error.value
    ? { label: "同步状态未知", pendingSince: "" }
    : routeSync(routes.value[hostId], gatewaySignal(hostId, "route_sync"));
}
</script>
<style scoped>
.page-head,
.host-heading,
.filters {
  display: flex;
  align-items: center;
  gap: var(--moox-space-3);
  flex-wrap: wrap;
}
.page-head,
.host-heading {
  justify-content: space-between;
}
.deployments-content {
  margin-top: var(--moox-space-3);
}
.filters {
  margin-bottom: var(--moox-space-2);
}
.filters :deep(.arco-input-wrapper) {
  width: 260px;
}
.filters :deep(.arco-select) {
  width: 220px;
}
.host-group {
  padding: 16px;
  margin-bottom: 16px;
  border: 1px solid var(--color-border-2);
  border-radius: 8px;
}
h3 {
  margin: 0 0 8px;
}
p {
  line-height: 1.6;
}
.muted,
small {
  color: var(--color-text-3);
}
small {
  display: block;
  margin-top: 4px;
}
.error {
  color: rgb(var(--danger-6));
  white-space: pre-wrap;
}
.hashes {
  font-family: monospace;
  font-size: 12px;
  color: var(--color-text-3);
  overflow-wrap: anywhere;
}
.table-scroll {
  overflow-x: auto;
}
table {
  width: 100%;
  border-collapse: collapse;
}
th,
td {
  padding: 10px 8px;
  text-align: left;
  vertical-align: top;
  border-bottom: 1px solid var(--color-border-2);
  overflow-wrap: anywhere;
}
th {
  white-space: nowrap;
  background: var(--color-fill-1);
}
.detail-link {
  padding: 0;
  color: rgb(var(--primary-6));
  cursor: pointer;
  background: none;
  border: 0;
  text-align: left;
}
.status-toggle {
  padding: 5px 10px;
  color: var(--color-text-1);
  cursor: pointer;
  background: var(--color-fill-2);
  border: 1px solid var(--color-border-2);
  border-radius: 4px;
  white-space: nowrap;
}
.status-toggle:disabled {
  cursor: not-allowed;
  opacity: 0.5;
}
.empty {
  padding: 24px;
  color: var(--color-text-3);
  text-align: center;
}
.component-detail {
  display: grid;
  grid-template-columns: 100px minmax(0, 1fr);
  gap: 12px;
}
.component-detail dd {
  margin: 0;
  overflow-wrap: anywhere;
}
.component-detail dt {
  color: var(--color-text-3);
}
.service-detail {
  padding: 12px 0;
  border-bottom: 1px solid var(--color-border-2);
  overflow-wrap: anywhere;
}
.method-detail {
  padding: 8px 0;
}
summary {
  cursor: pointer;
}
.locator-filter {
  margin-top: 8px;
}
</style>
