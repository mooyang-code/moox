<template>
  <div class="host-monitor-page">
    <div class="monitor-toolbar">
      <div class="refresh-status">
        <strong>主机资源状态</strong>
        <span>{{ lastRefreshAt ? `${formatAge(lastRefreshAt)}更新` : '等待首次更新' }}</span>
      </div>
      <div class="toolbar-actions">
        <a-tooltip content="刷新实时与历史数据">
          <a-button type="text" shape="circle" :loading="loading" aria-label="刷新主机监控" @click="manualRefresh">
            <template #icon><icon-refresh /></template>
          </a-button>
        </a-tooltip>
        <span class="auto-label">自动刷新</span>
        <a-switch v-model="autoRefresh" size="small" aria-label="自动刷新" />
        <a-radio-group v-model="viewMode" type="button" size="small" aria-label="监控视图">
          <a-radio value="cards"><icon-apps /> 卡片视图</a-radio>
          <a-radio value="master"><icon-list /> 主从视图</a-radio>
        </a-radio-group>
      </div>
    </div>

    <section class="monitor-summary" aria-label="主机监控总览">
      <div><span>主机</span><strong>{{ sshHosts.length }}</strong></div>
      <div><span>监控在线</span><strong class="healthy">{{ onlineCount }}</strong></div>
      <div><span>需关注</span><strong :class="{ danger: attentionCount > 0 }">{{ attentionCount }}</strong></div>
      <div><span>未接入监控</span><strong :class="{ warning: unmonitoredCount > 0 }">{{ unmonitoredCount }}</strong></div>
      <div><span>历史存储</span><strong :class="storageAvailable && !dataGap ? 'healthy' : 'danger'">{{ storageStatus }}</strong></div>
    </section>

    <a-alert v-if="loadError" type="warning" show-icon class="monitor-alert">{{ loadError }}</a-alert>

    <div v-if="!rows.length && loading" class="monitor-empty"><a-spin /></div>
    <a-empty v-else-if="!rows.length" description="暂无 SSH 主机或监控数据" class="monitor-empty" />
    <template v-else>
      <template v-if="viewMode === 'cards'">
        <HostMonitorCardGrid :rows="rows" :selected-key="selectedKey" @select="selectedKey = $event" />
        <HostMonitorDetail :row="selectedRow" :refresh-key="historyRefreshKey" />
      </template>
      <HostMonitorMasterDetail v-else :rows="rows" :selected-key="selectedKey" :selected-row="selectedRow" :refresh-key="historyRefreshKey" @select="selectedKey = $event" />
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue';
import { getCurrentMetrics, type HostMetrics } from '@/api/modules/host-monitor';
import { listSSHHosts, type SSHHost } from '@/api/modules/ssh';
import HostMonitorCardGrid from './host-monitor-card-grid.vue';
import HostMonitorMasterDetail from './host-monitor-master-detail.vue';
import HostMonitorDetail from './host-monitor-detail.vue';
import { buildHostMonitorRows, normalizeMonitorViewMode, type MonitorViewMode } from './host-monitor-mapping';

const AUTO_REFRESH_MS = 15_000;
const VIEW_MODE_KEY = 'moox.host-monitor.view-mode';
const loading = ref(false);
const autoRefresh = ref(true);
const monitors = ref<HostMetrics[]>([]);
const sshHosts = ref<SSHHost[]>([]);
const storageAvailable = ref(true);
const dataGap = ref(false);
const loadError = ref('');
const lastRefreshAt = ref('');
const selectedKey = ref('');
const historyRefreshKey = ref(0);
const viewMode = ref<MonitorViewMode>(normalizeMonitorViewMode(localStorage.getItem(VIEW_MODE_KEY)));
let refreshTimer: ReturnType<typeof setInterval> | null = null;
let requestID = 0;

const rows = computed(() => buildHostMonitorRows(monitors.value, sshHosts.value));
const selectedRow = computed(() => rows.value.find((row) => row.key === selectedKey.value) || null);
const onlineCount = computed(() => rows.value.filter((row) => row.state === 'online').length);
const attentionCount = computed(() => rows.value.filter((row) => row.attention || row.state === 'offline').length);
const unmonitoredCount = computed(() => rows.value.filter((row) => row.state === 'unmonitored').length);
const storageStatus = computed(() => !storageAvailable.value ? '不可用' : dataGap.value ? '存在缺口' : '正常');

async function refreshData(refreshHistory = false) {
  const currentRequestID = ++requestID;
  loading.value = true;
  const results = await Promise.allSettled([getCurrentMetrics(), listSSHHosts({ limit: 500 })]);
  if (currentRequestID !== requestID) return;
  const [monitorResult, sshResult] = results;
  const errors: string[] = [];
  if (monitorResult.status === 'fulfilled') {
    monitors.value = monitorResult.value.metrics;
    storageAvailable.value = monitorResult.value.storage_available;
    dataGap.value = monitorResult.value.data_gap;
  } else {
    errors.push('Monitor 实时数据加载失败，继续显示上次成功结果');
  }
  if (sshResult.status === 'fulfilled') {
    sshHosts.value = sshResult.value.hosts || [];
  } else {
    errors.push('SSH 主机清单加载失败，地址可能显示为 Agent ID');
  }
  loadError.value = errors.join('；');
  if (monitorResult.status === 'fulfilled' || sshResult.status === 'fulfilled') lastRefreshAt.value = new Date().toISOString();
  if (refreshHistory) historyRefreshKey.value++;
  loading.value = false;
}

function startAutoRefresh() {
  if (refreshTimer) clearInterval(refreshTimer);
  refreshTimer = setInterval(() => void refreshData(false), AUTO_REFRESH_MS);
}
function stopAutoRefresh() {
  if (refreshTimer) clearInterval(refreshTimer);
  refreshTimer = null;
}
function manualRefresh() { return refreshData(true); }
function formatAge(value: string) {
  const age = Date.now() - Date.parse(value);
  if (!Number.isFinite(age) || age < 60_000) return '刚刚';
  if (age < 3_600_000) return `${Math.floor(age / 60_000)} 分钟前`;
  return `${Math.floor(age / 3_600_000)} 小时前`;
}

watch(rows, (value) => {
  if (!value.length) selectedKey.value = '';
  else if (!value.some((row) => row.key === selectedKey.value)) selectedKey.value = value[0].key;
});
watch(viewMode, (value) => localStorage.setItem(VIEW_MODE_KEY, value));
watch(autoRefresh, (value) => value ? startAutoRefresh() : stopAutoRefresh());
onMounted(async () => { await refreshData(false); startAutoRefresh(); });
onUnmounted(() => { requestID++; stopAutoRefresh(); });
</script>

<style scoped lang="scss">
.host-monitor-page { min-height:0; padding:4px 0 20px; }
.monitor-toolbar { display:flex; align-items:center; justify-content:space-between; gap:16px; margin-bottom:10px; }
.refresh-status strong,.refresh-status span { display:block; }.refresh-status strong { font-size:14px; }.refresh-status span { margin-top:2px; color:var(--color-text-3); font-size:12px; }
.toolbar-actions { display:flex; align-items:center; gap:8px; }.auto-label { color:var(--color-text-3); font-size:12px; }
.monitor-summary { display:flex; flex-wrap:wrap; gap:14px 30px; padding:11px 0; margin-bottom:14px; border-top:1px solid var(--color-border-2); border-bottom:1px solid var(--color-border-2); }
.monitor-summary div { display:flex; align-items:baseline; gap:7px; min-width:90px; }.monitor-summary span { color:var(--color-text-3); font-size:12px; }.monitor-summary strong { font-size:16px; }.healthy { color:#16803c; }.warning { color:#d97706; }.danger { color:#dc2626; }
.monitor-alert { margin-bottom:12px; }.monitor-empty { display:flex; align-items:center; justify-content:center; min-height:260px; }
@media (max-width:760px) { .monitor-toolbar { align-items:flex-start; flex-direction:column; }.toolbar-actions { width:100%; flex-wrap:wrap; }.monitor-summary { gap:10px 18px; } }
</style>
