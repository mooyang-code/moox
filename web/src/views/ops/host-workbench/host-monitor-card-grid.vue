<template>
  <section class="monitor-card-grid" aria-label="主机监控卡片">
    <button
      v-for="row in rows"
      :key="row.key"
      type="button"
      class="monitor-card"
      :class="{ selected: row.key === selectedKey, attention: row.attention, offline: row.state !== 'online' }"
      :aria-pressed="row.key === selectedKey"
      :aria-label="`查看主机 ${row.displayName}`"
      @click="$emit('select', row.key)"
    >
      <div class="card-head">
        <div class="identity">
          <strong>{{ row.displayName }}</strong>
          <span>{{ row.displayAddress }}</span>
        </div>
        <span class="state" :class="row.state"><i />{{ stateText(row) }}</span>
      </div>
      <div class="last-seen">{{ lastSeenText(row) }}</div>
      <div class="metric-grid">
        <div><span>CPU</span><strong>{{ percent(row.cpuUsage) }}</strong></div>
        <div><span>内存</span><strong>{{ percent(row.memoryUsage) }}</strong></div>
        <div><span>文件系统</span><strong>{{ percent(row.filesystemUsage) }}</strong></div>
      </div>
      <div class="progress-list">
        <a-progress :percent="ratio(row.cpuUsage)" :show-text="false" size="small" :color="progressColor(row.cpuUsage)" />
        <a-progress :percent="ratio(row.memoryUsage)" :show-text="false" size="small" :color="progressColor(row.memoryUsage)" />
        <a-progress :percent="ratio(row.filesystemUsage)" :show-text="false" size="small" :color="progressColor(row.filesystemUsage)" />
      </div>
      <div class="network-row">
        <span><icon-arrow-down />{{ row.networkRate ? formatBytesPerSecond(row.networkRate.rx) : '--' }}</span>
        <span><icon-arrow-up />{{ row.networkRate ? formatBytesPerSecond(row.networkRate.tx) : '--' }}</span>
        <b>查看详情</b>
      </div>
    </button>
  </section>
</template>

<script setup lang="ts">
import { formatBytesPerSecond } from '@/api/modules/host-monitor';
import type { HostMonitorRow } from './host-monitor-mapping';

defineProps<{ rows: HostMonitorRow[]; selectedKey: string }>();
defineEmits<{ select: [key: string] }>();

const percent = (value: number | null) => value === null ? '--' : `${value}%`;
const ratio = (value: number | null) => value === null ? 0 : value / 100;
const progressColor = (value: number | null) => value !== null && value >= 80 ? '#dc2626' : value !== null && value >= 60 ? '#d97706' : '#16a34a';
const stateText = (row: HostMonitorRow) => row.state === 'online' ? '在线' : row.state === 'offline' ? '离线' : '未接入监控';
const lastSeenText = (row: HostMonitorRow) => {
  if (!row.monitor) return '尚未部署或配置 Host Agent';
  const age = Date.now() - Date.parse(row.monitor.timestamp);
  if (!Number.isFinite(age) || age < 0) return '刚刚上报';
  if (age < 60_000) return `${Math.max(1, Math.floor(age / 1_000))} 秒前上报`;
  if (age < 3_600_000) return `${Math.floor(age / 60_000)} 分钟前上报`;
  return `${Math.floor(age / 3_600_000)} 小时前上报`;
};
</script>

<style scoped lang="scss">
.monitor-card-grid { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); gap:var(--moox-space-3); }
.monitor-card { appearance:none; width:100%; min-height:210px; padding:14px; overflow:hidden; color:inherit; font:inherit; text-align:left; background:var(--color-bg-2); border:1px solid var(--color-border-2); border-radius:6px; cursor:pointer; transition:border-color .15s ease, box-shadow .15s ease; }
.monitor-card:hover { border-color:rgb(var(--primary-5)); }
.monitor-card:focus-visible { outline:2px solid rgb(var(--primary-6)); outline-offset:2px; }
.monitor-card.selected { border-color:rgb(var(--primary-6)); box-shadow:0 0 0 2px rgba(var(--primary-6),.1); }
.monitor-card.attention { border-left:3px solid #d97706; }
.monitor-card.offline { background:var(--color-fill-1); }
.card-head { display:flex; align-items:flex-start; justify-content:space-between; gap:var(--moox-space-3); }
.identity { min-width:0; }
.identity strong,.identity span { display:block; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.identity strong { font-size:15px; }
.identity span,.last-seen { color:var(--color-text-3); font-size:12px; }
.identity span { margin-top:3px; }
.last-seen { margin-top:var(--moox-space-1); }
.state { display:flex; align-items:center; gap:5px; flex:none; font-size:12px; }
.state i { width:7px; height:7px; border-radius:50%; background:#94a3b8; }
.state.online { color:#16803c; }.state.online i { background:#16a34a; }
.state.offline { color:#dc2626; }.state.offline i { background:#dc2626; }
.metric-grid { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); gap:var(--moox-space-2); margin-top:18px; }
.metric-grid div { min-width:0; padding:var(--moox-space-2); background:var(--color-fill-1); }
.metric-grid span,.metric-grid strong { display:block; }
.metric-grid span { color:var(--color-text-3); font-size:11px; }
.metric-grid strong { margin-top:3px; font-size:16px; font-variant-numeric:tabular-nums; }
.progress-list { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); gap:var(--moox-space-2); margin-top:6px; }
.network-row { display:flex; align-items:center; gap:14px; padding-top:var(--moox-space-3); margin-top:13px; border-top:1px solid var(--color-border-2); color:var(--color-text-2); font-size:12px; }
.network-row span { display:flex; align-items:center; gap:3px; min-width:0; }
.network-row b { margin-left:auto; color:rgb(var(--primary-6)); font-weight:500; }
@media (max-width:1180px) { .monitor-card-grid { grid-template-columns:repeat(2,minmax(0,1fr)); } }
@media (max-width:720px) { .monitor-card-grid { grid-template-columns:minmax(0,1fr); } }
@media (prefers-reduced-motion:reduce) { .monitor-card { transition:none; } }
</style>
