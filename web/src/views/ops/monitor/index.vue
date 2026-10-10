<template>
  <div class="moox-page health-monitor-page">
    <div class="moox-inner">
      <div class="health-toolbar">
        <div v-if="!props.embedded">
          <h2>监控告警</h2>
          <p class="muted">按部署主机、组件和数据链路查看当前状态</p>
        </div>
        <a-space
          ><span class="muted">告警推送：{{ notificationLabel }}</span
          ><a-button :loading="loading" @click="refresh">刷新</a-button
          ><a-button type="outline" @click="openNotification">消息通知设置</a-button></a-space
        >
      </div>
      <a-alert v-if="error" type="error" class="error">{{ error }}。以下保留上次成功加载的数据。</a-alert>
      <a-alert v-if="overviewLoaded && !overview.topology_known" type="warning"
        >部署清单尚未完成同步，当前无法判断完整的组件健康状态。</a-alert
      >
      <div v-if="overviewLoaded" class="summary-grid" aria-label="健康摘要">
        <div>
          <strong>{{ summary.alert_count || 0 }}</strong
          >当前告警
        </div>
        <div>
          <strong>{{ summary.attention_count || 0 }}</strong
          >需要关注
        </div>
        <div>
          <strong>{{ summary.healthy_count || 0 }}</strong
          >正常
        </div>
        <div>
          <strong>{{ summary.unknown_count || 0 }}</strong
          >未知
        </div>
      </div>
      <small>概览时间：{{ formatCheckedAt(overview.generated_at) }}</small>
      <section class="health-section">
        <h3>当前告警</h3>
        <div
          v-if="showClearState"
          class="clear-state"
          :class="systemHealthy ? 'clear-state--healthy' : 'clear-state--attention'"
          role="status"
          aria-live="polite"
        >
          <strong>{{ systemHealthy ? "系统一切正常" : "当前没有待处理告警" }}</strong
          ><span>{{ systemHealthy ? "已观测的组件、业务和主机均正常" : "仍有监控项未知、未探测或需要关注" }}</span>
        </div>
        <div class="item-grid">
          <a-card
            v-for="item in overview.alerts"
            :key="item.id"
            class="health-card--interactive"
            role="button"
            tabindex="0"
            @click="openAlert(item)"
            @keydown.enter.self.prevent="openAlert(item)"
            @keydown.space.self.prevent="openAlert(item)"
          >
            <template #title
              ><a-tag color="red">{{ item.severity }}</a-tag
              >{{ item.title }}</template
            >
            <p>{{ item.reason }}</p>
            <p class="muted">
              {{ item.object?.host_id || item.object?.agent_id }} {{ item.object?.component_id || item.object?.dataset_id }}
              {{ item.object?.freq }} · 持续 {{ formatDuration(item.triggered_at, overview.generated_at) }}
            </p>
            <a class="locator" :href="alertHref(item)" @click.stop>定位</a>
            <small>触发：{{ formatCheckedAt(item.triggered_at) }} · 最新检查：{{ formatCheckedAt(item.last_checked_at) }}</small>
          </a-card>
        </div>
      </section>

      <section class="health-section">
        <h3>组件</h3>
        <div class="table-scroll">
          <table class="component-matrix">
            <thead>
              <tr>
                <th>组件 / 主机</th>
                <th v-for="host in matrix.hosts" :key="host">{{ host }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="row in matrix.rows" :key="row.id">
                <th>
                  {{ row.name }}<small>{{ row.id }}</small>
                </th>
                <td v-for="host in matrix.hosts" :key="host">
                  <button
                    v-if="row.cells[host]"
                    class="health-card--interactive component-cell"
                    :class="`component-cell--${row.cells[host].status}`"
                    @click="openItem(row.cells[host])"
                  >
                    <span>{{ host }} · {{ row.id }}</span
                    ><a-tag :color="statusColor(row.cells[host].status)">{{ statusLabel(row.cells[host].status) }}</a-tag
                    ><small
                      >探测：{{ statusLabel(row.cells[host].probe?.status) }} / 上报：{{
                        statusLabel(row.cells[host].reporter?.status)
                      }}</small
                    ><small v-if="needsAttention(row.cells[host].status)">{{ row.cells[host].reason }}</small>
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
      <section class="health-section">
        <h3>数据链路</h3>
        <p class="muted">采集 → 存储与视图 → 因子 → 交易</p>
        <div class="pipeline-list">
          <details v-for="stage in overview.pipeline" :key="stage.id" class="pipeline-stage" :open="needsAttention(stage.status)">
            <summary>
              {{ stage.name }} <a-tag :color="statusColor(stage.status)">{{ statusLabel(stage.status) }}</a-tag>
            </summary>
            <div class="table-scroll">
              <table>
                <thead>
                  <tr>
                    <th>数据集 / 周期</th>
                    <th>状态</th>
                    <th>输入水位</th>
                    <th>输出水位</th>
                    <th>最近成功</th>
                    <th>落后</th>
                    <th>原因</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="item in stage.datasets" :key="`${item.producer}:${item.space_id}:${item.dataset_id}:${item.freq}`">
                    <td>
                      {{ item.space_id }} / {{ item.dataset_id }} / {{ item.freq }}<small>{{ item.producer }}</small>
                    </td>
                    <td>{{ statusLabel(item.status) }}</td>
                    <td>{{ formatCheckedAt(item.input_watermark_at) }}</td>
                    <td>{{ formatCheckedAt(item.output_watermark_at) }}</td>
                    <td>{{ formatCheckedAt(item.last_success_at) }}</td>
                    <td>{{ item.lag_seconds ?? 0 }} 秒</td>
                    <td>{{ item.reason }}<RawError :value="item.raw_error" /></td>
                  </tr>
                </tbody>
              </table>
            </div>
            <p v-if="!stage.datasets?.length" class="muted">暂无数据集观测</p>
          </details>
        </div>
      </section>
      <section class="health-section">
        <h3>业务检查</h3>
        <div class="item-grid">
          <details
            v-for="item in overview.business_checks"
            :key="`${item.space_id}:${item.check_id}`"
            class="pipeline-stage"
            :open="needsAttention(item.status)"
          >
            <summary>
              {{ item.kind }} · {{ item.module }} <a-tag :color="statusColor(item.status)">{{ statusLabel(item.status) }}</a-tag>
            </summary>
            <p>{{ item.space_id }} · {{ item.reason }}</p>
            <small>最新检查：{{ formatCheckedAt(item.checked_at) }}</small
            ><RawError :value="item.raw_error" />
          </details>
        </div>
      </section>
      <section class="health-section">
        <h3>主机</h3>
        <div class="host-list">
          <article v-for="item in overview.hosts" :key="`${item.host_id}:${item.agent_id}`" class="host-row">
            <div>
              <strong>{{ item.host_id || item.agent_id }}</strong
              ><a-tag :color="statusColor(item.status)">{{ statusLabel(item.status) }}</a-tag>
              <p>{{ item.hostname }} {{ item.address }}</p>
              <small v-if="!item.host_id">未关联部署主机</small>
            </div>
            <div>
              <p>{{ item.reason }}</p>
              <span
                >CPU {{ item.cpu_available ? `${item.cpu_percent?.toFixed(1)}%` : "暂无" }} · 内存
                {{ item.memory_available ? `${item.memory_percent?.toFixed(1)}%` : "暂无" }} · 磁盘
                {{ item.disk_available ? `${item.disk_percent?.toFixed(1)}%` : "暂无" }}</span
              ><small>最后上报：{{ formatCheckedAt(item.last_reported_at) }}</small>
            </div>
            <a :href="hostHref(item.agent_id, item.host_id)">主机监控</a>
            <details v-if="item.gateway_signals?.length" :open="needsAttention(item.status)">
              <summary>网关状态</summary>
              <div v-for="signal in item.gateway_signals" :key="signal.kind">
                <p>{{ signal.signal?.reason }} · {{ statusLabel(signal.signal?.status) }}</p>
                <RawError :value="signal.signal?.raw_error" />
              </div>
            </details>
          </article>
        </div>
      </section>
      <section class="health-section">
        <h3>未登记进程（{{ summary.unregistered_count || 0 }}）</h3>
        <div class="item-grid">
          <a-card
            v-for="item in overview.unregistered"
            :key="`${item.host_id}:${item.component_id}:${item.instances?.[0]?.instance_id}`"
            ><template #title>{{ item.name }}</template>
            <p>{{ item.host_id }} · {{ item.component_id }}</p>
            <p>{{ item.reason }}</p>
            <div v-for="instance in item.instances" :key="instance.instance_id">
              {{ instance.instance_id }} · {{ instance.version }} · {{ formatCheckedAt(instance.last_reported_at) }}
            </div></a-card
          >
        </div>
      </section>
    </div>
    <a-modal
      v-model:visible="notificationVisible"
      title="消息通知设置"
      @ok="saveNotification"
      @cancel="notificationVisible = false"
    >
      <a-form layout="vertical"
        ><a-form-item label="通知平台"
          ><a-select v-model="notification.channel_type"
            ><a-option value="wecom">企业微信</a-option><a-option value="feishu">飞书</a-option></a-select
          ></a-form-item
        ><a-form-item label="机器人 Webhook URL"
          ><a-input
            v-model="notification.webhook_url"
            @input="notification.url_changed = true"
            placeholder="输入新的 HTTPS Webhook URL" /></a-form-item
        ><a-checkbox v-model="notification.clear_url">清空当前 URL，停止站外通知</a-checkbox>
        <div class="muted">当前配置：{{ notification.masked || "未配置" }}</div></a-form
      >
    </a-modal>

    <a-drawer v-model:visible="detailVisible" title="监控详情" width="min(860px, 100vw)" :footer="false">
      <div v-if="selectedItem">
        <h3>{{ selectedItem.name }} · {{ selectedItem.host_id }}</h3>
        <p>{{ selectedItem.reason }}</p>
        <p>状态自：{{ formatCheckedAt(selectedItem.status_since) }}</p>
        <p>探测地址：{{ selectedItem.probe_url || "暂无" }}</p>
        <h4>探测 · {{ statusLabel(selectedItem.probe?.status) }}</h4>
        <p>{{ selectedItem.probe?.reason }}</p>
        <small>最新检查：{{ formatCheckedAt(selectedItem.probe?.checked_at) }}</small
        ><RawError :value="selectedItem.probe?.raw_error" />
        <details :open="needsAttention(selectedItem.probe?.status)">
          <summary>最近探测（{{ selectedItem.recent_probes?.length || 0 }} 次，最多 10 次）</summary>
          <div v-for="probe in selectedItem.recent_probes" :key="probe.checked_at">
            <p>{{ formatCheckedAt(probe.checked_at) }} · {{ statusLabel(probe.status) }} · {{ probe.reason }}</p>
            <RawError :value="probe.raw_error" />
          </div>
        </details>
        <h4>指标上报 · {{ statusLabel(selectedItem.reporter?.status) }}</h4>
        <p>{{ selectedItem.reporter?.reason }}</p>
        <small>最后上报：{{ formatCheckedAt(selectedItem.reporter?.checked_at) }}</small>
        <div class="table-scroll">
          <table>
            <thead>
              <tr>
                <th>实例</th>
                <th>启动标识</th>
                <th>版本</th>
                <th>状态</th>
                <th>最后上报</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="item in selectedItem.instances" :key="`${item.instance_id}:${item.boot_id}`">
                <td>{{ item.instance_id }}</td>
                <td>{{ item.boot_id }}</td>
                <td>{{ item.version }}</td>
                <td>{{ statusLabel(item.status) }}</td>
                <td>{{ formatCheckedAt(item.last_reported_at) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
      <div v-else-if="selectedAlert">
        <h3>{{ selectedAlert.title }}</h3>
        <p>{{ selectedAlert.reason }}</p>
        <dl>
          <dt>对象类型</dt>
          <dd>{{ selectedAlert.object?.type }}</dd>
          <dt>主机 / Agent</dt>
          <dd>{{ selectedAlert.object?.host_id }} / {{ selectedAlert.object?.agent_id }}</dd>
          <dt>组件 / 数据集</dt>
          <dd>
            {{ selectedAlert.object?.component_id }} {{ selectedAlert.object?.dataset_id }} {{ selectedAlert.object?.freq }}
          </dd>
          <dt>触发时间</dt>
          <dd>{{ formatCheckedAt(selectedAlert.triggered_at) }}</dd>
          <dt>最新检查</dt>
          <dd>{{ formatCheckedAt(selectedAlert.last_checked_at) }}</dd>
        </dl>
        <RawError :value="selectedAlert.raw_error" />
      </div>
    </a-drawer>
  </div>
</template>

<script setup lang="ts">
import { computed, onActivated, onDeactivated, onMounted, onUnmounted, reactive, ref } from "vue";
import { Message, Modal } from "@arco-design/web-vue";
import { monitorApi, type HealthAlert, type HealthComponent, type HealthOverview } from "@/api/monitor";
import { createLatestRequestGuard } from "@/utils/latest-request";
import { formatDuration, formatCheckedAt, statusColor, statusLabel } from "./health-display";
import RawError from "./raw-error.vue";

const props = defineProps<{ embedded?: boolean }>();
const overview = ref<HealthOverview>({});
const loading = ref(false);
const error = ref("");
const overviewLoaded = ref(false);
const notificationVisible = ref(false);
const detailVisible = ref(false);
const selectedItem = ref<HealthComponent | null>(null);
const selectedAlert = ref<HealthAlert | null>(null);
const notification = reactive({ channel_type: "wecom", webhook_url: "", masked: "", clear_url: false, url_changed: false });
let notificationInitialType = "wecom";
let timer: number | undefined;
const refreshGuard = createLatestRequestGuard();
const summary = computed(() => overview.value.summary || {});
const showClearState = computed(() => overviewLoaded.value && !error.value && !overview.value.alerts?.length);
const systemHealthy = computed(
  () =>
    overview.value.topology_known === true &&
    (summary.value.healthy_count || 0) > 0 &&
    !(
      summary.value.alert_count ||
      summary.value.attention_count ||
      summary.value.unknown_count ||
      summary.value.unregistered_count
    )
);

const needsAttention = (status?: string) => status === "down" || status === "degraded" || status === "unknown";
const notificationLabel = computed(() => {
  const setting = overview.value.notification;
  if (!setting?.configured) return "未配置";
  return ({ wecom: "企业微信", feishu: "飞书" }[setting.channel_type || ""] || setting.channel_type) + " ✓";
});
const matrix = computed(() => {
  const hosts = [...new Set((overview.value.components || []).map(item => item.host_id || ""))].sort();
  const rows = new Map<string, { id: string; name: string; cells: Record<string, HealthComponent> }>();
  for (const item of overview.value.components || []) {
    const id = item.component_id || "";
    if (!rows.has(id)) rows.set(id, { id, name: item.name || id, cells: {} });
    rows.get(id)!.cells[item.host_id || ""] = item;
  }
  return { hosts, rows: [...rows.values()].sort((a, b) => a.id.localeCompare(b.id)) };
});
function hostHref(agentID?: string, hostID?: string) {
  return (
    "#/ops/hosts?" +
    new URLSearchParams({ tab: "monitor", ...(agentID ? { agent_id: agentID } : {}), ...(hostID ? { host_id: hostID } : {}) })
  );
}
function alertHref(item: HealthAlert) {
  const object = item.object;
  if (object?.type === "host") return hostHref(object.agent_id, object.host_id);
  if (object?.type === "dataset")
    return object.producer === "factor" ? "#/factor/tasks?tab=results" : "#/collector/tasks?tab=results";
  return (
    "#/ops/deployments?" +
    new URLSearchParams({
      tab: "services",
      ...(object?.host_id ? { host_id: object.host_id } : {}),
      ...(object?.component_id ? { component_id: object.component_id } : {})
    })
  );
}
async function refresh() {
  const request = refreshGuard.begin();
  loading.value = true;
  error.value = "";
  try {
    const rsp = await monitorApi.getOverview();
    if (!rsp.overview) throw new Error("健康概览响应缺失");
    if (request.isLatest()) {
      overview.value = rsp.overview;
      overviewLoaded.value = true;
    }
  } catch (err) {
    if (request.isLatest()) error.value = err instanceof Error ? err.message : "健康数据加载失败";
  } finally {
    if (request.isLatest()) loading.value = false;
  }
}
function openItem(item: HealthComponent) {
  selectedAlert.value = null;
  selectedItem.value = item;
  detailVisible.value = true;
}
function openAlert(item: HealthAlert) {
  selectedItem.value = null;
  selectedAlert.value = item;
  detailVisible.value = true;
}
async function openNotification() {
  try {
    const rsp = await monitorApi.getNotification();
    const setting = rsp.channel || {};
    notification.channel_type = setting.channel_type || "wecom";
    notificationInitialType = notification.channel_type;
    notification.masked = setting.masked_url || "";
    notification.webhook_url = "";
    notification.clear_url = false;
    notification.url_changed = false;
    notificationVisible.value = true;
  } catch (err) {
    Message.error(err instanceof Error ? err.message : "通知配置加载失败");
  }
}
async function saveNotification() {
  const clearing = notification.clear_url || (notification.url_changed && !notification.webhook_url.trim());
  if (!notification.url_changed && !notification.clear_url && notification.channel_type === notificationInitialType) {
    notificationVisible.value = false;
    return;
  }
  if (!notification.url_changed && !notification.clear_url) {
    Message.warning("修改通知平台前，请重新输入 Webhook URL 或勾选清空");
    return;
  }
  if (clearing) {
    if (!notification.clear_url) notification.clear_url = true;
    Modal.warning({
      title: "确认停用通知",
      content: "清空后系统将不再向站外平台发送告警，是否继续？",
      onOk: () => void persistNotification()
    });
    return;
  }
  await persistNotification();
}
async function persistNotification() {
  try {
    const rsp = await monitorApi.updateNotification({
      channel_type: notification.channel_type,
      webhook_url: notification.clear_url ? "" : notification.webhook_url
    });
    overview.value.notification = rsp.channel;
    notification.masked = rsp.channel?.masked_url || "";
    notification.webhook_url = "";
    notification.clear_url = false;
    notification.url_changed = false;
    notificationInitialType = notification.channel_type;
    notificationVisible.value = false;
    Message.success("通知配置已保存");
  } catch (err) {
    Message.error(err instanceof Error ? err.message : "通知配置保存失败");
  }
}

function startPolling() {
  if (timer) return;
  timer = window.setInterval(() => {
    if (!notificationVisible.value) void refresh();
  }, 30000);
}
function stopPolling() {
  if (timer) window.clearInterval(timer);
  timer = undefined;
  refreshGuard.invalidate();
  loading.value = false;
}
onMounted(() => {
  void refresh();
  startPolling();
});
onActivated(() => {
  if (!timer) {
    void refresh();
    startPolling();
  }
});
onDeactivated(stopPolling);
onUnmounted(stopPolling);
</script>

<style scoped lang="scss">
.health-monitor-page {
  height: 100%;
  overflow: auto;
}
.health-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  flex-wrap: wrap;
}
h2,
h3 {
  margin: 0;
}
h3 {
  margin-bottom: 12px;
}
.muted,
small {
  color: var(--color-text-3);
}
.error {
  margin: 16px 0;
}
.health-section {
  margin-top: 24px;
}
.summary-grid,
.item-grid {
  display: grid;
  gap: 12px;
  grid-template-columns: repeat(auto-fit, minmax(min(100%, 280px), 1fr));
}
.summary-grid {
  margin: 18px 0;
  grid-template-columns: repeat(4, 1fr);
}
.summary-grid div {
  padding: 16px;
  background: var(--color-fill-1);
  border-radius: 8px;
}
.summary-grid strong {
  display: block;
  font-size: 24px;
}
.health-card--interactive {
  cursor: pointer;
}
.health-card--interactive:focus-visible {
  outline: 2px solid rgb(var(--primary-6));
  outline-offset: 2px;
}
.clear-state {
  display: flex;
  gap: 12px;
  padding: 16px;
  margin-bottom: 12px;
  border-radius: 8px;
  flex-wrap: wrap;
}
.clear-state--healthy {
  background: rgb(var(--green-1));
  color: rgb(var(--green-8));
}
.clear-state--attention {
  background: rgb(var(--orange-1));
  color: rgb(var(--orange-8));
}
.host-row {
  display: flex;
  align-items: center;
  gap: 20px;
  flex-wrap: wrap;
  padding: 12px;
  border-bottom: 1px solid var(--color-border-2);
}
.host-row small {
  display: block;
}
.component-cell {
  display: grid;
  gap: 8px;
  text-align: left;
  cursor: pointer;
  width: 100%;
  padding: 10px;
  border: 1px solid var(--color-border-2);
  border-radius: 6px;
  background: var(--color-fill-1);
  color: var(--color-text-1);
}
.component-cell--down {
  border-color: rgb(var(--red-4));
}
.component-matrix th small {
  display: block;
  font-weight: normal;
}
.locator {
  display: block;
  margin: 8px 0;
}
.pipeline-list {
  display: grid;
  gap: 12px;
}
.pipeline-stage {
  border: 1px solid var(--color-border-2);
  border-radius: 8px;
  padding: 16px;
}
.dataset-detail {
  margin: 12px 0;
  overflow-wrap: anywhere;
}
summary {
  cursor: pointer;
}
dl {
  display: grid;
  grid-template-columns: auto 1fr;
  gap: 8px;
}
dd {
  margin: 0;
  overflow-wrap: anywhere;
}
.table-scroll {
  overflow: auto;
  margin-top: 16px;
}
table {
  width: 100%;
  border-collapse: collapse;
}
th,
td {
  padding: 8px;
  text-align: left;
  border-bottom: 1px solid var(--color-border-2);
}
@media (max-width: 600px) {
  .summary-grid {
    grid-template-columns: repeat(2, 1fr);
  }
}
</style>
