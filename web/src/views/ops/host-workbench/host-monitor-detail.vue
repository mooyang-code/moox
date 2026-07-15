<template>
  <section v-if="row" class="monitor-detail" aria-label="主机监控详情">
    <header class="detail-head">
      <div>
        <span>当前主机</span>
        <h3>{{ row.displayName }}</h3>
        <p>{{ row.displayAddress }}</p>
      </div>
      <a-radio-group v-if="row.monitor" v-model="duration" type="button" size="small">
        <a-radio value="1h">1小时</a-radio>
        <a-radio value="24h">24小时</a-radio>
        <a-radio value="3d">3天</a-radio>
      </a-radio-group>
    </header>

    <a-alert v-if="!row.monitor" type="info" show-icon>该主机尚未接入监控，请部署并配置 MooX Host Agent。</a-alert>
    <template v-else>
      <a-alert v-if="row.state !== 'online'" type="warning" show-icon class="detail-alert">主机当前离线，设备明细来自最后一次成功上报。</a-alert>
      <a-alert v-if="historyNotice" type="warning" show-icon class="detail-alert">{{ historyNotice }}</a-alert>

      <div class="detail-overview">
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
          <dl>
            <div><dt>CPU 核心</dt><dd>{{ row.monitor.cpu.cores || '--' }}</dd></div>
            <div><dt>内存容量</dt><dd>{{ formatBytes(row.monitor.memory.total) }}</dd></div>
            <div><dt>文件系统</dt><dd>{{ row.monitor.filesystems.length }}</dd></div>
            <div><dt>磁盘 / 网卡</dt><dd>{{ row.monitor.disks.length }} / {{ row.monitor.networks.length }}</dd></div>
          </dl>
        </section>
      </div>

      <div class="tables-grid">
        <section class="data-section">
          <h4>文件系统</h4>
          <div class="table-scroll">
            <table class="detail-table filesystem-table">
              <colgroup><col style="width:28%"><col style="width:25%"><col style="width:15%"><col style="width:14%"><col style="width:18%"></colgroup>
              <thead><tr><th>挂载点</th><th>设备</th><th>类型</th><th>使用率</th><th>容量</th></tr></thead>
              <tbody>
                <tr v-for="item in row.monitor.filesystems" :key="`${item.device}:${item.mountpoint}`">
                  <td :title="item.mountpoint">{{ item.mountpoint || '--' }}</td><td :title="item.device">{{ item.device || '--' }}</td><td :title="item.fs_type">{{ item.fs_type || '--' }}</td><td :title="item.percent_available ? `${item.percent.toFixed(1)}%` : '--'">{{ item.percent_available ? `${item.percent.toFixed(1)}%` : '--' }}</td><td :title="formatBytes(item.total)">{{ formatBytes(item.total) }}</td>
                </tr>
                <tr v-if="!row.monitor.filesystems.length"><td colspan="5" class="empty-cell">暂无文件系统数据</td></tr>
              </tbody>
            </table>
          </div>
        </section>
        <section class="data-section">
          <h4>磁盘 I/O</h4>
          <div class="table-scroll">
            <table class="detail-table disk-table">
              <colgroup><col style="width:14%"><col style="width:18%"><col style="width:18%"><col style="width:17%"><col style="width:17%"><col style="width:16%"></colgroup>
              <thead><tr><th>设备</th><th>读取</th><th>写入</th><th>读取 IOPS</th><th>写入 IOPS</th><th>利用率</th></tr></thead>
              <tbody>
                <tr v-for="item in row.monitor.disks" :key="item.device">
                  <td :title="item.device">{{ item.device || '--' }}</td><td :title="item.rate_available ? formatBytesPerSecond(item.read_bytes_per_second) : '--'">{{ item.rate_available ? formatBytesPerSecond(item.read_bytes_per_second) : '--' }}</td><td :title="item.rate_available ? formatBytesPerSecond(item.write_bytes_per_second) : '--'">{{ item.rate_available ? formatBytesPerSecond(item.write_bytes_per_second) : '--' }}</td><td :title="item.rate_available ? item.read_iops.toFixed(1) : '--'">{{ item.rate_available ? item.read_iops.toFixed(1) : '--' }}</td><td :title="item.rate_available ? item.write_iops.toFixed(1) : '--'">{{ item.rate_available ? item.write_iops.toFixed(1) : '--' }}</td><td :title="item.rate_available ? `${item.utilization_percent.toFixed(1)}%` : '--'">{{ item.rate_available ? `${item.utilization_percent.toFixed(1)}%` : '--' }}</td>
                </tr>
                <tr v-if="!row.monitor.disks.length"><td colspan="6" class="empty-cell">暂无磁盘数据</td></tr>
              </tbody>
            </table>
          </div>
        </section>
        <section class="data-section">
          <h4>网络接口</h4>
          <div class="table-scroll">
            <table class="detail-table network-table">
              <colgroup><col style="width:18%"><col style="width:18%"><col style="width:25%"><col style="width:25%"><col style="width:14%"></colgroup>
              <thead><tr><th>接口</th><th>状态</th><th>接收</th><th>发送</th><th>错误</th></tr></thead>
              <tbody>
                <tr v-for="item in row.monitor.networks" :key="item.device">
                  <td :title="item.device">{{ item.device || '--' }}</td><td :title="item.operstate"><span class="operstate" :class="item.operstate">{{ item.operstate }}</span></td><td :title="item.rate_available ? formatBytesPerSecond(item.rx_speed) : '--'">{{ item.rate_available ? formatBytesPerSecond(item.rx_speed) : '--' }}</td><td :title="item.rate_available ? formatBytesPerSecond(item.tx_speed) : '--'">{{ item.rate_available ? formatBytesPerSecond(item.tx_speed) : '--' }}</td><td :title="String(item.receive_errors_total + item.transmit_errors_total)">{{ item.receive_errors_total + item.transmit_errors_total }}</td>
                </tr>
                <tr v-if="!row.monitor.networks.length"><td colspan="5" class="empty-cell">暂无网络数据</td></tr>
              </tbody>
            </table>
          </div>
        </section>
      </div>
    </template>
  </section>
</template>

<script setup lang="ts">
import { nextTick, onUnmounted, ref, watch } from 'vue';
import { default as VChart } from '@visactor/vchart';
import { formatBytes, formatBytesPerSecond, getHistoryMetrics, historyHasRenderableData, type HistoryPoint } from '@/api/modules/host-monitor';
import type { HostMonitorRow } from './host-monitor-mapping';

const props = defineProps<{ row: HostMonitorRow | null; refreshKey?: number }>();
const duration = ref('1h');
const historyLoading = ref(false);
const historyData = ref<HistoryPoint[]>([]);
const historyNotice = ref('');
const trendChartRef = ref<HTMLElement>();
let trendChart: VChart | null = null;
let historyRequestID = 0;

function renderChart() {
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
    color: ['#2563eb', '#16a34a', '#d97706'], line: { style: { lineWidth: 2, curveType: 'monotone' } }, point: { visible: false },
    legends: { visible: true, orient: 'top' }, axes: [{ orient: 'left', min: 0, max: 100 }, { orient: 'bottom', sampling: true }],
    tooltip: { mark: { content: [{ key: (datum: any) => datum.type, value: (datum: any) => `${datum.value.toFixed(1)}%` }] } },
    crosshair: { xField: { visible: true } },
  } as any, { dom: trendChartRef.value });
  trendChart.renderSync();
}

async function loadHistory() {
  const monitor = props.row?.monitor;
  const requestID = ++historyRequestID;
  trendChart?.release();
  trendChart = null;
  historyData.value = [];
  historyNotice.value = '';
  if (!monitor) return;
  historyLoading.value = true;
  try {
    const response = await getHistoryMetrics(monitor.host_id, duration.value);
    if (requestID !== historyRequestID) return;
    historyData.value = response.history;
    historyNotice.value = !response.storage_available ? '历史存储暂不可用' : response.data_gap ? '当前时间范围存在历史数据缺口' : '';
    await nextTick();
    renderChart();
  } catch {
    if (requestID === historyRequestID) historyNotice.value = '历史数据加载失败';
  } finally {
    if (requestID === historyRequestID) historyLoading.value = false;
  }
}

watch([() => props.row?.monitor?.host_id, duration, () => props.refreshKey], loadHistory, { immediate: true });
onUnmounted(() => { historyRequestID++; trendChart?.release(); });
</script>

<style scoped lang="scss">
.monitor-detail { min-width:0; padding-top:20px; margin-top:20px; border-top:1px solid var(--color-border-2); }
.detail-head { display:flex; align-items:flex-end; justify-content:space-between; gap:16px; margin-bottom:14px; }
.detail-head span,.detail-head p { color:var(--color-text-3); font-size:12px; }
.detail-head h3 { margin:2px 0; font-size:18px; }.detail-head p { margin:0; }
.detail-alert { margin-bottom:14px; }
.detail-overview { display:grid; grid-template-columns:minmax(0,2fr) minmax(250px,1fr); gap:20px; }
h4 { margin:0 0 10px; font-size:14px; }
.chart-container { position:relative; width:100%; height:270px; border-top:1px solid var(--color-border-2); }
.chart-overlay { position:absolute; inset:0; z-index:2; display:flex; align-items:center; justify-content:center; background:var(--color-bg-1); }
.empty-chart { flex-direction:column; gap:6px; color:var(--color-text-3); font-size:28px; }.empty-chart span { font-size:12px; }
.device-section dl { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:1px; margin:0; background:var(--color-border-2); border:1px solid var(--color-border-2); }
.device-section dl div { padding:14px; background:var(--color-bg-2); }.device-section dt { color:var(--color-text-3); font-size:12px; }.device-section dd { margin:5px 0 0; font-size:16px; font-weight:600; }
.tables-grid { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); gap:18px; margin-top:20px; }
.data-section { min-width:0; }
.table-scroll { max-height:300px; overflow-x:hidden; overflow-y:auto; border-top:1px solid var(--color-border-2); }
.detail-table { width:100%; table-layout:fixed; border-collapse:collapse; font-size:11px; }
th,td { padding:9px 5px; overflow:hidden; text-align:left; text-overflow:ellipsis; white-space:nowrap; border-bottom:1px solid var(--color-border-2); font-variant-numeric:tabular-nums; }
th { position:sticky; top:0; z-index:1; color:var(--color-text-3); font-weight:500; background:var(--color-bg-2); }
.empty-cell { color:var(--color-text-3); text-align:center; }.operstate.up { color:#16803c; }
@media (max-width:1180px) { .tables-grid { grid-template-columns:minmax(0,1fr); } }
@media (max-width:760px) { .detail-head { align-items:flex-start; flex-direction:column; }.detail-overview { grid-template-columns:minmax(0,1fr); } }
</style>
