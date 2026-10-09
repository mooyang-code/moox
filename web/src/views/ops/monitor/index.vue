<template>
  <div class="moox-page monitor-page">
    <div class="moox-inner">
      <div class="page-head">
        <div class="page-title">
          <h2>监控告警</h2>
          <span class="overall" :class="overallClass" role="status" aria-live="polite">{{ sentence }}</span>
        </div>
        <a-space wrap>
          <span class="push-state">
            告警推送：{{ pushLabel }}
            <icon-check-circle-fill v-if="overview.notification?.configured" class="push-state__ok" />
          </span>
          <a-button size="small" @click="notificationVisible = true">
            <template #icon><icon-notification /></template>
            推送设置
          </a-button>
          <a-button size="small" :loading="loading" @click="refresh">
            <template #icon><icon-refresh /></template>
            刷新
          </a-button>
        </a-space>
      </div>

      <a-alert v-if="error" type="error" class="monitor-alert">{{ error }}</a-alert>
      <a-alert v-for="warning in overview.warnings || []" :key="warning" type="warning" class="monitor-alert">
        {{ warning }}
      </a-alert>

      <section class="monitor-section" aria-label="当前告警">
        <div class="section-title">
          <h3>当前告警</h3>
          <span>{{ alerts.length }} 条</span>
        </div>
        <div v-if="loaded && !alerts.length" class="alert-clear">
          <icon-check-circle-fill class="alert-clear__icon" />
          当前没有告警
        </div>
        <div v-else class="alert-list">
          <div v-for="alert in alerts" :key="alert.id" class="alert-row" :class="`alert-row--${alert.severity}`">
            <a-tag :color="severityColor(alert.severity)">{{ severityLabel(alert.severity) }}</a-tag>
            <div class="alert-main">
              <div class="alert-head">
                <span class="alert-target">
                  {{ targetKindLabel(alert.target?.kind) }} · {{ targetLabel(alert.target, components) }}
                </span>
                <strong>{{ alert.title }}</strong>
              </div>
              <div class="alert-reason">{{ alert.reason }}</div>
              <RawError :text="alert.raw_error" />
            </div>
            <span class="alert-duration">持续 {{ formatSince(alert.triggered_at, now) || "—" }}</span>
            <a-button v-if="locateRoute(alert, hosts)" size="mini" @click="locate(alert)">定位</a-button>
          </div>
        </div>
      </section>

      <section class="monitor-section" aria-label="数据链路">
        <div class="section-title">
          <h3>数据链路</h3>
          <span>点击阶段查看明细</span>
        </div>
        <div class="pipeline">
          <template v-for="(stage, index) in pipeline" :key="stage.stage">
            <button
              type="button"
              class="stage-card"
              :class="[`stage-card--${stage.status}`, { 'stage-card--active': stage.stage === activeStage }]"
              :aria-expanded="stage.stage === activeStage"
              @click="toggleStage(stage.stage)"
            >
              <span class="stage-card__name">{{ stage.name }}</span>
              <a-tag size="small" :color="statusColor(stage.status)">{{ statusLabel(stage.status) }}</a-tag>
              <small>{{ stageSummary(stage) }}</small>
            </button>
            <icon-right v-if="index < pipeline.length - 1" class="stage-arrow" aria-hidden="true" />
          </template>
        </div>
        <div v-if="selectedStage" class="stage-detail">
          <a-table
            v-if="selectedStage.datasets?.length"
            :data="selectedStage.datasets"
            :pagination="false"
            size="small"
            :bordered="{ cell: true }"
            :scroll="{ x: 'max-content' }"
            :row-key="datasetRowKey"
          >
            <template #columns>
              <a-table-column title="数据集" :width="260">
                <template #cell="{ record }">
                  <div>{{ record.dataset_id }}</div>
                  <small class="muted">{{ record.space_id }} · {{ record.producer }}</small>
                </template>
              </a-table-column>
              <a-table-column title="频率" data-index="frequency" :width="70" />
              <a-table-column title="状态" :width="90">
                <template #cell="{ record }">
                  <a-tag size="small" :color="statusColor(record.status)">{{ statusLabel(record.status) }}</a-tag>
                </template>
              </a-table-column>
              <a-table-column title="最新水位" :width="170">
                <template #cell="{ record }">{{ formatTime(record.watermark_at) }}</template>
              </a-table-column>
              <a-table-column title="落后" :width="110">
                <template #cell="{ record }">{{ formatLag(record.lag_seconds) }}</template>
              </a-table-column>
              <a-table-column title="最近成功" :width="170">
                <template #cell="{ record }">{{ formatTime(record.last_success_at) }}</template>
              </a-table-column>
              <a-table-column title="原因" :width="320">
                <template #cell="{ record }">
                  <div>{{ record.reason }}</div>
                  <RawError :text="record.raw_error" />
                </template>
              </a-table-column>
            </template>
          </a-table>
          <div v-for="check in stageChecks" :key="`${check.kind}:${check.module}`" class="stage-check">
            <a-tag size="small" :color="statusColor(check.status)">{{ statusLabel(check.status) }}</a-tag>
            <strong>{{ check.name }}</strong>
            <span class="muted">{{ check.reason }}</span>
          </div>
          <div v-for="alert in stageAlerts" :key="alert.id" class="stage-check">
            <a-tag size="small" :color="severityColor(alert.severity)">告警</a-tag>
            <strong>{{ alert.title }}</strong>
            <span class="muted">{{ alert.reason }}</span>
          </div>
          <a-empty
            v-if="!selectedStage.datasets?.length && !stageChecks.length && !stageAlerts.length"
            description="这个阶段暂无监控数据"
          />
        </div>
      </section>

      <section class="monitor-section" aria-label="服务">
        <div class="section-title">
          <h3>服务</h3>
          <span>{{ components.length }} 个部署 · {{ componentAttention }} 个需关注</span>
        </div>
        <a-empty v-if="loaded && !matrix.rows.length" description="暂无部署" />
        <div v-else class="matrix-wrap">
          <table class="component-matrix">
            <thead>
              <tr>
                <th scope="col">组件</th>
                <th v-for="host in matrix.hosts" :key="host" scope="col">{{ host }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="row in matrix.rows" :key="row.componentId" :class="{ 'matrix-row--attention': isAttention(row.status) }">
                <th scope="row">
                  <div class="matrix-name">{{ row.name }}</div>
                  <small class="muted">{{ row.componentId }}</small>
                </th>
                <td v-for="host in matrix.hosts" :key="host">
                  <button
                    v-if="row.cells[host]"
                    type="button"
                    class="matrix-cell"
                    :class="`matrix-cell--${row.cells[host].status}`"
                    :title="row.cells[host].reason"
                    @click="openComponent(row.cells[host])"
                  >
                    <span class="status-dot" aria-hidden="true" />
                    <span>{{ statusLabel(row.cells[host].status) }}</span>
                    <span v-if="isAttention(row.cells[host].status)" class="matrix-cell__reason">
                      {{ row.cells[host].reason }}
                    </span>
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section class="monitor-section" aria-label="主机">
        <div class="section-title">
          <h3>主机</h3>
          <span>{{ hosts.length }} 台</span>
        </div>
        <a-table
          :data="hosts"
          :pagination="false"
          size="small"
          row-key="host_id"
          :bordered="{ cell: true }"
          :scroll="{ x: 'max-content' }"
        >
          <template #columns>
            <a-table-column title="主机" data-index="host_id" :width="140" />
            <a-table-column title="状态" :width="90">
              <template #cell="{ record }">
                <a-tag size="small" :color="statusColor(record.status)">{{ statusLabel(record.status) }}</a-tag>
              </template>
            </a-table-column>
            <a-table-column title="说明" :width="320">
              <template #cell="{ record }">{{ record.reason }}</template>
            </a-table-column>
            <a-table-column title="主机网关" :width="100">
              <template #cell="{ record }">{{ gatewayStateLabel(record.gateway_state) }}</template>
            </a-table-column>
            <a-table-column title="CPU" :width="80">
              <template #cell="{ record }">{{ record.agent_id ? formatPercent(record.cpu_percent) : "—" }}</template>
            </a-table-column>
            <a-table-column title="内存" :width="80">
              <template #cell="{ record }">{{ record.agent_id ? formatPercent(record.memory_percent) : "—" }}</template>
            </a-table-column>
            <a-table-column title="磁盘" :width="80">
              <template #cell="{ record }">{{ record.agent_id ? formatPercent(record.disk_percent) : "—" }}</template>
            </a-table-column>
            <a-table-column title="最近上报" :width="170">
              <template #cell="{ record }">{{ formatTime(record.last_seen_at) }}</template>
            </a-table-column>
            <a-table-column title="操作" :width="100" fixed="right">
              <template #cell="{ record }">
                <a-link @click="openHostMonitor(record)">主机监控</a-link>
              </template>
            </a-table-column>
          </template>
        </a-table>
      </section>

      <section class="monitor-section" aria-label="业务检查">
        <div class="section-title">
          <h3>业务检查</h3>
          <span>{{ businessChecks.length }} 项</span>
        </div>
        <a-table
          :data="businessChecks"
          :pagination="false"
          size="small"
          :row-key="businessRowKey"
          :bordered="{ cell: true }"
          :scroll="{ x: 'max-content' }"
        >
          <template #columns>
            <a-table-column title="名称" data-index="name" :width="240" />
            <a-table-column title="状态" :width="90">
              <template #cell="{ record }">
                <a-tag size="small" :color="statusColor(record.status)">{{ statusLabel(record.status) }}</a-tag>
              </template>
            </a-table-column>
            <a-table-column title="原因" :width="360">
              <template #cell="{ record }">
                <div>{{ record.reason }}</div>
                <RawError :text="record.raw_error" />
              </template>
            </a-table-column>
            <a-table-column title="空间" data-index="space_id" :width="100" />
            <a-table-column title="检查时间" :width="170">
              <template #cell="{ record }">{{ formatTime(record.checked_at) }}</template>
            </a-table-column>
          </template>
        </a-table>
      </section>

      <section v-if="unregistered.length" class="monitor-section" aria-label="未登记的进程">
        <div class="section-title">
          <h3>未登记的进程</h3>
          <span>{{ unregistered.length }} 个</span>
        </div>
        <a-alert type="warning" class="monitor-alert">
          这些进程在上报运行指标，但没有登记部署，Monitor 不接收它们的上报。请在 moox.toml 的部署表中登记，或停掉这些进程。
        </a-alert>
        <a-table
          :data="unregistered"
          :pagination="false"
          size="small"
          row-key="instance_id"
          :bordered="{ cell: true }"
          :scroll="{ x: 'max-content' }"
        >
          <template #columns>
            <a-table-column title="主机" data-index="host_id" :width="140" />
            <a-table-column title="组件" data-index="component_id" :width="160" />
            <a-table-column title="实例" data-index="instance_id" :width="220" />
            <a-table-column title="版本" data-index="version" :width="120" />
            <a-table-column title="最近上报" :width="170">
              <template #cell="{ record }">{{ formatTime(record.last_seen_at) }}</template>
            </a-table-column>
          </template>
        </a-table>
      </section>
    </div>

    <ComponentDrawer v-model:visible="drawerVisible" :component="selectedComponent" :now="now" />
    <NotificationModal v-model:visible="notificationVisible" @saved="refresh" />
  </div>
</template>

<script setup lang="ts">
import { computed, onActivated, onDeactivated, onMounted, onUnmounted, ref } from "vue";
import { useRouter } from "vue-router";
import { IconCheckCircleFill, IconNotification, IconRefresh, IconRight } from "@arco-design/web-vue/es/icon";
import {
  monitorApi,
  type HealthAlert,
  type HealthBusinessCheck,
  type HealthComponent,
  type HealthHost,
  type HealthOverview,
  type HealthPipelineDataset,
  type HealthPipelineStage
} from "@/api/monitor";
import { createLatestRequestGuard } from "@/utils/latest-request";
import ComponentDrawer from "./component-drawer.vue";
import NotificationModal from "./notification-modal.vue";
import RawError from "./raw-error.vue";
import {
  buildComponentMatrix,
  channelLabel,
  formatLag,
  formatPercent,
  formatSince,
  formatTime,
  gatewayStateLabel,
  isAttention,
  locateRoute,
  overallSentence,
  severityColor,
  severityLabel,
  stageCounts,
  statusColor,
  statusLabel,
  statusRank,
  targetKindLabel,
  targetLabel
} from "./monitor-display";

const POLL_INTERVAL_MS = 30_000;
const router = useRouter();
const overview = ref<HealthOverview>({});
const loading = ref(false);
const loaded = ref(false);
const error = ref("");
const now = ref(new Date());
const notificationVisible = ref(false);
const drawerVisible = ref(false);
const selectedComponent = ref<HealthComponent | null>(null);
const chosenStage = ref("");
const refreshGuard = createLatestRequestGuard();
let timer: number | undefined;

const alerts = computed(() => overview.value.alerts || []);
const components = computed(() => overview.value.components || []);
const hosts = computed(() => overview.value.hosts || []);
const pipeline = computed(() => overview.value.pipeline || []);
const businessChecks = computed(() => overview.value.business_checks || []);
const unregistered = computed(() => overview.value.unregistered || []);
const matrix = computed(() => buildComponentMatrix(components.value));
const componentAttention = computed(() => components.value.filter(item => isAttention(item.status)).length);
const sentence = computed(() => (loaded.value ? overallSentence(overview.value) : "正在加载…"));
const overallClass = computed(() => {
  if (!loaded.value) return "";
  if (alerts.value.length || overview.value.summary?.attention) return "overall--attention";
  return "overall--healthy";
});
const pushLabel = computed(() =>
  overview.value.notification?.configured ? channelLabel(overview.value.notification.channel_type) : "未配置"
);
// 没有手动选过阶段时，默认展开最严重的异常阶段。
const activeStage = computed(() => {
  if (chosenStage.value) return chosenStage.value === "none" ? "" : chosenStage.value;
  const worst = [...pipeline.value].sort((left, right) => statusRank(right.status) - statusRank(left.status))[0];
  return worst && isAttention(worst.status) ? worst.stage || "" : "";
});
const selectedStage = computed(() => pipeline.value.find(stage => stage.stage === activeStage.value));
const stageChecks = computed(() => businessChecks.value.filter(check => check.stage && check.stage === activeStage.value));
const stageAlerts = computed(() =>
  alerts.value.filter(alert => alert.stage && alert.stage === activeStage.value && alert.target?.kind !== "dataset")
);

function stageSummary(stage: HealthPipelineStage) {
  const counts = stageCounts(stage);
  if (!counts.total) return "暂无数据集";
  return counts.attention ? `${counts.total} 个数据集，${counts.attention} 个需关注` : `${counts.total} 个数据集`;
}

function toggleStage(stage?: string) {
  chosenStage.value = stage && stage !== activeStage.value ? stage : "none";
}

function datasetRowKey(record: HealthPipelineDataset) {
  return `${record.space_id}:${record.dataset_id}:${record.frequency}:${record.producer}`;
}

function businessRowKey(record: HealthBusinessCheck) {
  return `${record.kind}:${record.module}:${record.space_id}`;
}

function openComponent(component: HealthComponent) {
  selectedComponent.value = component;
  drawerVisible.value = true;
}

function locate(alert: HealthAlert) {
  const target = locateRoute(alert, hosts.value);
  if (target) void router.push(target);
}

function openHostMonitor(host: HealthHost) {
  void router.push({ path: "/ops/hosts", query: host.agent_id ? { tab: "monitor", agent: host.agent_id } : { tab: "monitor" } });
}

async function refresh() {
  const request = refreshGuard.begin();
  loading.value = true;
  error.value = "";
  try {
    const rsp = await monitorApi.getOverview();
    if (!request.isLatest()) return;
    overview.value = rsp.overview || {};
    now.value = new Date();
    loaded.value = true;
  } catch (err) {
    if (request.isLatest()) error.value = err instanceof Error ? err.message : "监控数据加载失败";
  } finally {
    if (request.isLatest()) loading.value = false;
  }
}

function startPolling() {
  if (timer) return;
  timer = window.setInterval(() => {
    if (!notificationVisible.value) void refresh();
  }, POLL_INTERVAL_MS);
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
</script>

<style scoped lang="scss">
.monitor-page {
  height: 100%;
  overflow: auto;
}

.page-head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: var(--moox-space-3);
  margin-bottom: var(--moox-space-2);
}

.page-title {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: var(--moox-space-3);
}

.page-head h2 {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
}

.overall {
  color: var(--color-text-2);
}

.overall--healthy {
  color: rgb(var(--green-6));
}

.overall--attention {
  color: rgb(var(--orange-6));
}

.push-state {
  display: inline-flex;
  align-items: center;
  gap: var(--moox-space-1);
  color: var(--color-text-2);
}

.push-state__ok {
  color: rgb(var(--green-6));
}

.monitor-alert {
  margin-bottom: var(--moox-space-2);
}

.monitor-section {
  margin-top: var(--moox-space-5);
}

.section-title {
  display: flex;
  align-items: baseline;
  gap: var(--moox-space-3);
  margin-bottom: var(--moox-space-3);
}

.section-title h3 {
  margin: 0;
  font-size: 16px;
}

.section-title span,
.muted {
  color: var(--color-text-3);
}

.alert-clear {
  display: flex;
  align-items: center;
  gap: var(--moox-space-2);
  padding: var(--moox-space-2) var(--moox-space-3);
  border: 1px solid rgb(var(--green-3));
  border-radius: 6px;
  background: rgb(var(--green-1));
  color: var(--color-text-1);
}

.alert-clear__icon {
  color: rgb(var(--green-6));
}

.alert-list {
  display: grid;
  gap: var(--moox-space-2);
}

.alert-row {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto auto;
  align-items: start;
  gap: var(--moox-space-3);
  padding: var(--moox-space-2) var(--moox-space-3);
  border: 1px solid var(--color-border-2);
  border-left: 3px solid rgb(var(--red-6));
  border-radius: 6px;
  background: var(--color-bg-2);
}

.alert-row--warning {
  border-left-color: rgb(var(--orange-6));
}

.alert-head {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: var(--moox-space-2);
}

.alert-target {
  color: var(--color-text-3);
  font-size: 12px;
}

.alert-reason {
  margin-top: 2px;
  color: var(--color-text-2);
}

.alert-duration {
  color: var(--color-text-3);
  white-space: nowrap;
}

.pipeline {
  display: flex;
  align-items: stretch;
  gap: var(--moox-space-2);
  overflow-x: auto;
}

.stage-card {
  display: grid;
  flex: 1 1 0;
  justify-items: start;
  gap: var(--moox-space-1);
  min-width: 150px;
  padding: var(--moox-space-3);
  border: 1px solid var(--color-border-2);
  border-radius: 6px;
  background: var(--color-bg-2);
  color: inherit;
  text-align: left;
  cursor: pointer;
  transition:
    border-color 0.16s ease,
    box-shadow 0.16s ease;
}

.stage-card:hover,
.stage-card:focus-visible,
.stage-card--active {
  border-color: rgb(var(--primary-6));
  box-shadow: 0 0 0 2px rgb(var(--primary-1));
  outline: none;
}

.stage-card--down {
  border-left: 3px solid rgb(var(--red-6));
}

.stage-card--degraded {
  border-left: 3px solid rgb(var(--orange-6));
}

.stage-card__name {
  font-weight: 600;
}

.stage-card small {
  color: var(--color-text-3);
}

.stage-arrow {
  flex: 0 0 auto;
  align-self: center;
  color: var(--color-text-4);
}

.stage-detail {
  display: grid;
  gap: var(--moox-space-2);
  margin-top: var(--moox-space-3);
}

.stage-check {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--moox-space-2);
}

.matrix-wrap {
  overflow-x: auto;
  border: 1px solid var(--color-border-2);
  border-radius: 6px;
}

.component-matrix {
  width: 100%;
  border-collapse: collapse;
}

.component-matrix th,
.component-matrix td {
  padding: var(--moox-space-2);
  border-bottom: 1px solid var(--color-border-1);
  text-align: left;
  vertical-align: top;
}

.component-matrix thead th {
  background: var(--color-fill-1);
  color: var(--color-text-2);
  font-weight: 500;
  white-space: nowrap;
}

.component-matrix tbody th {
  min-width: 160px;
  font-weight: 400;
}

.matrix-name {
  color: var(--color-text-1);
}

.matrix-row--attention th {
  box-shadow: inset 3px 0 0 rgb(var(--orange-6));
}

.matrix-cell {
  display: inline-flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  max-width: 280px;
  padding: 2px 8px;
  border: 1px solid transparent;
  border-radius: 4px;
  background: var(--color-fill-1);
  color: var(--color-text-2);
  font-size: 12px;
  text-align: left;
  cursor: pointer;
}

.matrix-cell:hover,
.matrix-cell:focus-visible {
  border-color: rgb(var(--primary-6));
  outline: none;
}

.matrix-cell__reason {
  flex-basis: 100%;
  color: var(--color-text-2);
}

.status-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--color-text-4);
}

.matrix-cell--healthy .status-dot {
  background: rgb(var(--green-6));
}

.matrix-cell--degraded {
  background: rgb(var(--orange-1));
}

.matrix-cell--degraded .status-dot {
  background: rgb(var(--orange-6));
}

.matrix-cell--down {
  background: rgb(var(--red-1));
}

.matrix-cell--down .status-dot {
  background: rgb(var(--red-6));
}

.matrix-cell--unchecked .status-dot {
  background: rgb(var(--arcoblue-5));
}

.matrix-cell--disabled {
  color: var(--color-text-4);
}

@media (max-width: 768px) {
  .alert-row {
    grid-template-columns: auto minmax(0, 1fr);
  }

  .alert-duration {
    grid-column: 2;
  }
}
</style>
