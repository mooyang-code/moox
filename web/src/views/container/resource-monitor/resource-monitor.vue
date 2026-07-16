<template>
  <div class="moox-page resource-monitor-page">
    <header class="page-header">
      <div>
        <h2>主机监控</h2>
        <p>CPU、内存、文件系统、磁盘与网络状态</p>
      </div>
      <div class="refresh-controls">
        <span v-if="lastRefreshAt" class="refresh-time">更新于 {{ formatAge(lastRefreshAt) }}</span>
        <a-tooltip content="刷新实时与历史数据">
          <a-button type="text" shape="circle" :loading="loading" aria-label="刷新" @click="manualRefresh">
            <template #icon><icon-refresh /></template>
          </a-button>
        </a-tooltip>
        <a-switch v-model="autoRefresh" size="small" aria-label="自动刷新" @change="toggleAutoRefresh" />
      </div>
    </header>

    <section class="summary-band" aria-label="主机状态总览">
      <div class="summary-item"><span>在线</span><strong>{{ onlineHosts }} / {{ hostMetrics.length }}</strong></div>
      <div class="summary-item"><span>需关注</span><strong :class="{ danger: attentionHosts > 0 }">{{ attentionHosts }}</strong></div>
      <div class="summary-item">
        <span>历史存储</span>
        <strong :class="storageAvailable && !currentDataGap ? 'healthy' : 'danger'">
          {{ !storageAvailable ? '不可用' : currentDataGap ? '存在缺口' : '正常' }}
        </strong>
      </div>
      <div class="summary-item"><span>刷新模式</span><strong>{{ autoRefresh ? '自动 · 5秒' : '手动' }}</strong></div>
    </section>

    <a-alert v-if="refreshError" type="warning" :show-icon="true" class="page-alert">{{ refreshError }}</a-alert>

    <section v-if="hostCards.length" class="host-grid" aria-label="主机列表">
      <button
        v-for="host in hostCards"
        :key="host.host_id"
        type="button"
        class="host-card"
        :class="{ selected: selectedHostID === host.host_id, warning: host.attention, offline: host.status !== 'online' }"
        :aria-pressed="selectedHostID === host.host_id"
        :aria-label="`查看主机 ${host.host_name}`"
        @click="selectHost(host.host_id)"
      >
        <div class="host-card-header">
          <div class="host-identity">
            <strong>{{ host.host_name }}</strong>
            <span>{{ host.address }}</span>
          </div>
          <span class="host-status" :class="host.status">
            <i />{{ statusText(host.status) }}
          </span>
        </div>
        <div class="last-seen">{{ host.timestamp ? `${formatAge(host.timestamp)}上报` : '尚未上报' }}</div>

        <div class="metric-list">
          <div class="metric-row">
            <span>CPU</span>
            <a-progress :percent="host.cpuAvailable ? host.cpuUsage / 100 : 0" :show-text="false" size="small" :color="progressColor(host.cpuUsage)" aria-hidden="true" />
            <strong>{{ host.cpuAvailable ? `${host.cpuUsage}%` : '--' }}</strong>
          </div>
          <div class="metric-row">
            <span>内存</span>
            <a-progress :percent="host.memoryAvailable ? host.memoryUsage / 100 : 0" :show-text="false" size="small" :color="progressColor(host.memoryUsage)" aria-hidden="true" />
            <strong>{{ host.memoryAvailable ? `${host.memoryUsage}%` : '--' }}</strong>
          </div>
          <div class="metric-row">
            <span>文件系统</span>
            <a-progress :percent="host.filesystemUsage !== null ? host.filesystemUsage / 100 : 0" :show-text="false" size="small" :color="progressColor(host.filesystemUsage ?? 0)" aria-hidden="true" />
            <strong>{{ host.filesystemUsage !== null ? `${host.filesystemUsage}%` : '--' }}</strong>
          </div>
        </div>

        <div class="network-summary">
          <span><icon-arrow-down />{{ host.networkRate ? formatBytesPerSecond(host.networkRate.rx) : '--' }}</span>
          <span><icon-arrow-up />{{ host.networkRate ? formatBytesPerSecond(host.networkRate.tx) : '--' }}</span>
        </div>
      </button>
    </section>

    <a-empty v-else-if="!loading" description="暂无主机上报" class="empty-state" />
    <div v-else class="loading-state"><a-spin /></div>

    <section v-if="selectedHost" class="detail-area">
      <div class="detail-heading">
        <div>
          <span class="eyebrow">当前主机</span>
          <h3>{{ selectedHost.host_name }}</h3>
        </div>
        <a-radio-group v-model="historyDuration" type="button" size="small" @change="loadHistory">
          <a-radio value="1h">1小时</a-radio>
          <a-radio value="24h">24小时</a-radio>
          <a-radio value="3d">3天</a-radio>
        </a-radio-group>
      </div>

      <a-alert v-if="historyNotice" type="warning" :show-icon="true" class="history-alert">{{ historyNotice }}</a-alert>
      <a-alert v-if="selectedHost.status !== 'online'" type="info" :show-icon="true" class="history-alert">
        主机当前离线，设备明细来自最后一次成功上报。
      </a-alert>

      <div class="detail-grid">
        <section class="trend-section">
          <h4>资源趋势</h4>
          <div ref="trendChartRef" class="chart-container">
            <div v-if="historyLoading" class="chart-overlay"><a-spin /></div>
            <div v-else-if="!historyHasRenderableData(historyData)" class="chart-overlay empty-chart">
              <icon-bar-chart /><span>暂无历史数据</span>
            </div>
          </div>
        </section>

        <section class="device-section">
          <h4>设备概览</h4>
          <dl class="device-summary">
            <div><dt>CPU 核心</dt><dd>{{ selectedHost.cpu.cores || '--' }}</dd></div>
            <div><dt>内存</dt><dd>{{ formatBytes(selectedHost.memory.total) }}</dd></div>
            <div><dt>文件系统</dt><dd>{{ selectedHost.filesystems.length }}</dd></div>
            <div><dt>磁盘 / 网卡</dt><dd>{{ selectedHost.disks.length }} / {{ selectedHost.networks.length }}</dd></div>
          </dl>
        </section>
      </div>

      <div class="tables-grid">
        <section class="data-section">
          <h4>文件系统</h4>
          <div class="table-scroll">
            <table>
              <thead><tr><th>挂载点</th><th>设备</th><th>类型</th><th>使用率</th><th>容量</th></tr></thead>
              <tbody>
                <tr v-for="item in selectedHost.filesystems" :key="`${item.device}:${item.mountpoint}`">
                  <td :title="item.mountpoint">{{ item.mountpoint || '--' }}</td>
                  <td :title="item.device">{{ item.device || '--' }}</td>
                  <td>{{ item.fs_type || '--' }}</td>
                  <td>{{ item.percent_available ? `${item.percent.toFixed(1)}%` : '--' }}</td>
                  <td>{{ formatBytes(item.total) }}</td>
                </tr>
                <tr v-if="!selectedHost.filesystems.length"><td colspan="5" class="table-empty">暂无文件系统数据</td></tr>
              </tbody>
            </table>
          </div>
        </section>

        <section class="data-section">
          <h4>磁盘 I/O</h4>
          <div class="table-scroll">
            <table>
              <thead><tr><th>设备</th><th>读取</th><th>写入</th><th>利用率</th></tr></thead>
              <tbody>
                <tr v-for="item in selectedHost.disks" :key="item.device">
                  <td>{{ item.device || '--' }}</td>
                  <td>{{ item.rate_available ? formatBytesPerSecond(item.read_bytes_per_second) : '--' }}</td>
                  <td>{{ item.rate_available ? formatBytesPerSecond(item.write_bytes_per_second) : '--' }}</td>
                  <td>{{ item.rate_available ? `${item.utilization_percent.toFixed(1)}%` : '--' }}</td>
                </tr>
                <tr v-if="!selectedHost.disks.length"><td colspan="4" class="table-empty">暂无磁盘数据</td></tr>
              </tbody>
            </table>
          </div>
        </section>

        <section class="data-section">
          <h4>网络接口</h4>
          <div class="table-scroll">
            <table>
              <thead><tr><th>接口</th><th>状态</th><th>接收</th><th>发送</th><th>错误</th></tr></thead>
              <tbody>
                <tr v-for="item in selectedHost.networks" :key="item.device">
                  <td>{{ item.device || '--' }}</td>
                  <td><span class="operstate" :class="item.operstate">{{ item.operstate }}</span></td>
                  <td>{{ item.rate_available ? formatBytesPerSecond(item.rx_speed) : '--' }}</td>
                  <td>{{ item.rate_available ? formatBytesPerSecond(item.tx_speed) : '--' }}</td>
                  <td>{{ item.receive_errors_total + item.transmit_errors_total }}</td>
                </tr>
                <tr v-if="!selectedHost.networks.length"><td colspan="5" class="table-empty">暂无网络数据</td></tr>
              </tbody>
            </table>
          </div>
        </section>
      </div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue';
import { Message } from '@arco-design/web-vue';
import { default as VChart } from '@visactor/vchart';
import {
  aggregateNetworkRate,
  formatBytes,
  formatBytesPerSecond,
  getCurrentMetrics,
  getHistoryMetrics,
  historyHasRenderableData,
  maxAvailableFilesystemUsage,
  metricValueAvailable,
  type HistoryPoint,
  type HostMetrics,
} from '@/api/modules/host-monitor';

const loading = ref(false);
const historyLoading = ref(false);
const autoRefresh = ref(true);
const hostMetrics = ref<HostMetrics[]>([]);
const storageAvailable = ref(true);
const currentDataGap = ref(false);
const refreshError = ref('');
const lastRefreshAt = ref('');
const selectedHostID = ref('');
const historyDuration = ref('1h');
const historyData = ref<HistoryPoint[]>([]);
const historyNotice = ref('');
const trendChartRef = ref<HTMLElement>();
let refreshTimer: ReturnType<typeof setInterval> | null = null;
let trendChart: VChart | null = null;
let currentRequestID = 0;
let historyRequestID = 0;

const hostCards = computed(() => hostMetrics.value.map((host) => {
  const filesystemUsage = maxAvailableFilesystemUsage(host.filesystems);
  const networkRate = aggregateNetworkRate(host.networks);
  const cpuUsage = Math.round(host.cpu.usage);
  const memoryUsage = Math.round(host.memory.percent);
  const cpuAvailable = metricValueAvailable(host.status, host.cpu.usage_available);
  const memoryAvailable = metricValueAvailable(host.status, host.memory.percent_available);
  const filesystemAvailable = metricValueAvailable(host.status, filesystemUsage !== null);
  const attention = [cpuAvailable ? cpuUsage : 0, memoryAvailable ? memoryUsage : 0, filesystemAvailable ? filesystemUsage ?? 0 : 0].some((value) => value >= 80);
  return {
    ...host,
    cpuUsage,
    memoryUsage,
    cpuAvailable,
    memoryAvailable,
    filesystemUsage: filesystemAvailable && filesystemUsage !== null ? Math.round(filesystemUsage) : null,
    networkRate: metricValueAvailable(host.status, networkRate !== null) ? networkRate : null,
    attention,
  };
}));

const selectedHost = computed(() => hostMetrics.value.find((host) => host.host_id === selectedHostID.value) ?? null);
const onlineHosts = computed(() => hostMetrics.value.filter((host) => host.status === 'online').length);
const attentionHosts = computed(() => hostCards.value.filter((host) => host.attention || host.status !== 'online').length);

const refreshData = async (silent = false) => {
  const requestID = ++currentRequestID;
  loading.value = true;
  try {
    const response = await getCurrentMetrics();
    if (requestID !== currentRequestID) return;
    hostMetrics.value = response.metrics;
    storageAvailable.value = response.storage_available;
    currentDataGap.value = response.data_gap;
    lastRefreshAt.value = new Date().toISOString();
    refreshError.value = '';
    if (!silent) Message.success('主机数据已刷新');
  } catch {
    if (requestID !== currentRequestID) return;
    refreshError.value = '实时数据刷新失败，当前仍显示上一次成功结果。请检查 Monitor 服务后重试。';
    if (!silent) Message.error('主机数据刷新失败');
  } finally {
    if (requestID === currentRequestID) loading.value = false;
  }
};

const loadHistory = async () => {
  if (!selectedHostID.value) return;
  const requestID = ++historyRequestID;
  const agentID = selectedHostID.value;
  const duration = historyDuration.value;
  historyLoading.value = true;
  try {
    const response = await getHistoryMetrics(agentID, duration);
    if (requestID !== historyRequestID || agentID !== selectedHostID.value || duration !== historyDuration.value) return;
    historyData.value = response.history;
    historyNotice.value = !response.storage_available ? '历史存储暂不可用' : response.data_gap ? '当前时间范围存在历史数据缺口' : '';
    await nextTick();
    renderTrendChart();
  } catch {
    if (requestID !== historyRequestID) return;
    historyNotice.value = '历史数据加载失败';
  } finally {
    if (requestID === historyRequestID) historyLoading.value = false;
  }
};

const selectHost = (hostID: string) => {
  if (selectedHostID.value === hostID) return;
  selectedHostID.value = hostID;
  loadHistory();
};

const manualRefresh = async () => {
  await refreshData(false);
  await loadHistory();
};

const startAutoRefresh = () => {
  if (refreshTimer) clearInterval(refreshTimer);
  refreshTimer = setInterval(() => refreshData(true), 5_000);
};

const stopAutoRefresh = () => {
  if (!refreshTimer) return;
  clearInterval(refreshTimer);
  refreshTimer = null;
};

const toggleAutoRefresh = (enabled: boolean) => enabled ? startAutoRefresh() : stopAutoRefresh();

const renderTrendChart = () => {
  trendChart?.release();
  trendChart = null;
  if (!trendChartRef.value || !historyHasRenderableData(historyData.value)) return;
  const values: Array<{ time: string; value: number; type: string }> = [];
  for (const point of historyData.value) {
    const time = new Date(point.timestamp).toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' });
    if (point.cpu_available) values.push({ time, value: point.cpu_usage, type: 'CPU' });
    if (point.memory_available) values.push({ time, value: point.memory_percent, type: '内存' });
    if (point.disk_available) values.push({ time, value: point.disk_percent, type: '文件系统' });
  }
  trendChart = new VChart({
    type: 'line', data: [{ id: 'trend', values }], xField: 'time', yField: 'value', seriesField: 'type',
    color: ['#2563eb', '#16a34a', '#f59e0b'],
    line: { style: { lineWidth: 2, curveType: 'monotone' } }, point: { visible: false },
    legends: { visible: true, orient: 'top' },
    axes: [{ orient: 'left', min: 0, max: 100 }, { orient: 'bottom', sampling: true }],
    tooltip: { mark: { content: [{ key: (datum: any) => datum.type, value: (datum: any) => `${datum.value.toFixed(1)}%` }] } },
    crosshair: { xField: { visible: true } },
  } as any, { dom: trendChartRef.value });
  trendChart.renderSync();
};

const progressColor = (value: number) => value >= 80 ? '#dc2626' : value >= 60 ? '#f59e0b' : '#16a34a';
const statusText = (status: HostMetrics['status']) => status === 'online' ? '在线' : status === 'offline' ? '离线' : '异常';
const formatAge = (value: string) => {
  const age = Date.now() - Date.parse(value);
  if (!Number.isFinite(age) || age < 0) return '刚刚';
  if (age < 60_000) return `${Math.max(1, Math.floor(age / 1_000))}秒前`;
  if (age < 3_600_000) return `${Math.floor(age / 60_000)}分钟前`;
  return `${Math.floor(age / 3_600_000)}小时前`;
};

watch(hostCards, (hosts) => {
  if (!hosts.length) {
    historyRequestID++;
    selectedHostID.value = '';
    historyData.value = [];
    return;
  }
  if (!hosts.some((host) => host.host_id === selectedHostID.value)) {
    selectedHostID.value = hosts[0].host_id;
    loadHistory();
  }
});

onMounted(async () => {
  await refreshData(true);
  startAutoRefresh();
});

onUnmounted(() => {
  stopAutoRefresh();
  trendChart?.release();
});
</script>

<style lang="scss" scoped>
.resource-monitor-page { padding: 16px; }
.page-header { display:flex; align-items:flex-start; justify-content:space-between; gap:16px; margin-bottom:8px; }
.page-header h2 { margin:0; font-size:20px; font-weight:600; }
.page-header p { margin:6px 0 0; color:var(--color-text-2); }
.refresh-controls { display:flex; align-items:center; gap:10px; min-height:32px; }
.refresh-time { color:var(--color-text-3); font-size:12px; }
.summary-band { display:flex; flex-wrap:wrap; gap:28px; padding:14px 0; border-top:1px solid var(--color-border-2); border-bottom:1px solid var(--color-border-2); margin-bottom:8px; }
.summary-item { display:flex; align-items:baseline; gap:8px; min-width:130px; }
.summary-item span { color:var(--color-text-2); font-size:13px; }
.summary-item strong { color:var(--color-text-1); font-size:16px; }
.healthy { color:#16a34a !important; }
.danger { color:#dc2626 !important; }
.page-alert, .history-alert { margin-bottom:8px; }
.host-grid { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); gap:16px; }
.host-card { appearance:none; width:100%; min-height:252px; padding:16px; text-align:left; color:inherit; font:inherit; background:var(--color-bg-2); border:1px solid var(--color-border-2); border-radius:8px; cursor:pointer; touch-action:manipulation; transition:border-color .15s ease, box-shadow .15s ease; }
.host-card:hover { border-color:rgb(var(--primary-5)); }
.host-card:focus-visible { outline:2px solid rgb(var(--primary-6)); outline-offset:2px; }
.host-card.selected { border-color:rgb(var(--primary-6)); box-shadow:0 0 0 2px rgba(var(--primary-6),.12); }
.host-card.warning { border-left:3px solid #f59e0b; }
.host-card.offline { opacity:.76; }
.host-card-header { display:flex; justify-content:space-between; gap:12px; }
.host-identity { min-width:0; }
.host-identity strong, .host-identity span { display:block; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.host-identity strong { font-size:16px; }
.host-identity span, .last-seen { color:var(--color-text-3); font-size:12px; }
.last-seen { margin-top:4px; }
.host-status { display:flex; align-items:center; gap:6px; flex:none; font-size:12px; }
.host-status i { width:7px; height:7px; border-radius:50%; background:#94a3b8; }
.host-status.online { color:#16a34a; }.host-status.online i { background:#16a34a; }
.host-status.offline, .host-status.error { color:#dc2626; }.host-status.offline i, .host-status.error i { background:#dc2626; }
.metric-list { display:grid; gap:12px; margin-top:20px; }
.metric-row { display:grid; grid-template-columns:58px minmax(0,1fr) 44px; align-items:center; gap:10px; font-size:13px; }
.metric-row strong { text-align:right; font-variant-numeric:tabular-nums; }
.network-summary { display:flex; justify-content:space-between; gap:12px; padding-top:14px; margin-top:16px; border-top:1px solid var(--color-border-2); color:var(--color-text-2); font-size:12px; }
.network-summary span { display:flex; align-items:center; gap:4px; min-width:0; }
.detail-area { margin-top:28px; }
.detail-heading { display:flex; align-items:end; justify-content:space-between; gap:16px; margin-bottom:14px; }
.detail-heading h3 { margin:2px 0 0; font-size:20px; }
.eyebrow { color:var(--color-text-3); font-size:12px; }
.detail-grid { display:grid; grid-template-columns:minmax(0,2fr) minmax(260px,1fr); gap:20px; }
.trend-section, .device-section, .data-section { min-width:0; }
.trend-section h4, .device-section h4, .data-section h4 { margin:0 0 12px; font-size:14px; }
.chart-container { position:relative; height:280px; border-top:1px solid var(--color-border-2); }
.chart-overlay { position:absolute; inset:0; display:flex; align-items:center; justify-content:center; z-index:2; background:var(--color-bg-1); }
.empty-chart { flex-direction:column; gap:8px; color:var(--color-text-3); font-size:28px; }
.empty-chart span { font-size:13px; }
.device-summary { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:1px; margin:0; background:var(--color-border-2); border:1px solid var(--color-border-2); }
.device-summary div { padding:16px; background:var(--color-bg-2); }
.device-summary dt { color:var(--color-text-3); font-size:12px; }
.device-summary dd { margin:6px 0 0; font-size:16px; font-weight:600; }
.tables-grid { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); gap:20px; margin-top:24px; }
.table-scroll { overflow:auto; border-top:1px solid var(--color-border-2); }
table { width:100%; min-width:440px; border-collapse:collapse; font-size:12px; }
th, td { padding:10px 8px; text-align:left; border-bottom:1px solid var(--color-border-2); white-space:nowrap; }
th { color:var(--color-text-3); font-weight:500; }
td { max-width:150px; overflow:hidden; text-overflow:ellipsis; font-variant-numeric:tabular-nums; }
.table-empty { text-align:center; color:var(--color-text-3); }
.operstate { color:var(--color-text-3); }.operstate.up { color:#16a34a; }
.empty-state, .loading-state { display:flex; align-items:center; justify-content:center; min-height:260px; }
@media (max-width:1200px) { .host-grid { grid-template-columns:repeat(2,minmax(0,1fr)); } .tables-grid { grid-template-columns:1fr; } }
@media (max-width:820px) { .resource-monitor-page { padding:12px; } .page-header, .detail-heading { align-items:flex-start; flex-direction:column; } .host-grid, .detail-grid { grid-template-columns:minmax(0,1fr); } .summary-band { gap:12px 20px; } .summary-item { min-width:110px; } }
@media (max-width:560px) { .host-grid { grid-template-columns:minmax(0,1fr); } .refresh-time { display:none; } .network-summary { flex-direction:column; } }
@media (prefers-reduced-motion:reduce) { .host-card { transition:none; } }
</style>
