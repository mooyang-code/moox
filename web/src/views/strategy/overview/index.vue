<template>
  <div class="strategy-page">
    <div class="moox-inner">
      <div class="page-head">
        <div>
          <h2>策略运行概览</h2>
          <span>查看当前策略的运行健康、目标偏差和绩效来源。</span>
        </div>
        <a-button :loading="store.loading" @click="refresh"><template #icon><icon-refresh /></template>刷新</a-button>
      </div>

      <a-alert v-if="store.error" type="error" show-icon class="top-alert">{{ store.error }}</a-alert>

      <a-grid :cols="5" :col-gap="12" :row-gap="12" class="summary-grid">
        <a-grid-item v-for="item in summary" :key="item.label">
          <a-card class="summary-card" :bordered="false">
            <a-statistic :title="item.label" :value="item.value" />
          </a-card>
        </a-grid-item>
      </a-grid>

      <a-card class="table-card" :bordered="false">
        <div class="filters">
          <a-input v-model="filters.strategy_id" allow-clear placeholder="策略 ID" style="width: 180px" @press-enter="refresh" />
          <a-select v-model="filters.mode" allow-clear placeholder="运行模式" style="width: 140px" @change="refresh">
            <a-option value="observe">Observe</a-option><a-option value="paper">Paper</a-option><a-option value="live">Live</a-option>
          </a-select>
          <a-select v-model="filters.status" allow-clear placeholder="状态" style="width: 140px" @change="refresh">
            <a-option value="enabled">enabled</a-option><a-option value="disabled">disabled</a-option><a-option value="failed">failed</a-option>
          </a-select>
          <a-button type="primary" @click="refresh">查询</a-button>
        </div>
        <a-table row-key="binding_id" size="small" :loading="store.loading" :data="store.rows" :pagination="pagination" @page-change="onPageChange" @page-size-change="onPageSizeChange">
          <template #columns>
            <a-table-column title="策略" :width="180"><template #cell="{ record }"><a-link @click="openDetail(record.binding_id)">{{ record.strategy_id }}@{{ record.version }}</a-link></template></a-table-column>
            <a-table-column title="Binding" data-index="binding_id" :width="150" />
            <a-table-column title="模式" :width="100"><template #cell="{ record }"><a-tag size="small" :color="modeColor(record.mode)">{{ record.mode }}</a-tag></template></a-table-column>
            <a-table-column title="状态" :width="130"><template #cell="{ record }"><StatusBadge :status="record.health?.status || record.status" /></template></a-table-column>
            <a-table-column title="版本 Hash" :width="170" :ellipsis="true" :tooltip="true"><template #cell="{ record }">{{ shortHash(record.source_hash) }}</template></a-table-column>
            <a-table-column title="最近运行" :width="180"><template #cell="{ record }">{{ formatTime(record.health?.observed_at) }}</template></a-table-column>
            <a-table-column title="数据版本" data-index="last_data_revision" :width="180" :ellipsis="true" :tooltip="true" />
            <a-table-column title="操作" :width="100"><template #cell="{ record }"><a-button size="mini" type="text" @click="openDetail(record.binding_id)">查看</a-button></template></a-table-column>
          </template>
        </a-table>
      </a-card>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive } from 'vue';
import { useRouter } from 'vue-router';
import { useStrategyStore } from '@/store/modules/strategy';
import StatusBadge from '@/views/strategy/components/strategy-status-badge.vue';
import type { RunningStrategySummary } from '@/api/strategy-types';

defineOptions({ name: 'StrategyOverview' });
const router = useRouter();
const store = useStrategyStore();
const filters = reactive({ strategy_id: '', mode: '', status: '' });
const pagination = reactive({ current: 1, pageSize: 20, total: 0, showTotal: true, showPageSize: true });
const summary = computed(() => {
  const rows: RunningStrategySummary[] = store.rows;
  return [
    { label: '运行中策略', value: store.total },
    { label: '正常运行', value: rows.filter((row) => ['enabled', 'running'].includes(row.health?.status || row.status)).length },
    { label: '异常策略', value: rows.filter((row) => ['failed', 'unknown', 'stale'].includes(row.health?.status || row.status)).length },
    { label: 'Paper', value: rows.filter((row) => row.mode === 'paper').length },
    { label: 'Live', value: rows.filter((row) => row.mode === 'live').length },
  ];
});

async function refresh() {
  await store.loadRunning({ ...filters, page: pagination.current, page_size: pagination.pageSize });
  pagination.total = store.total;
}
function onPageChange(page: number) { pagination.current = page; refresh(); }
function onPageSizeChange(size: number) { pagination.pageSize = size; pagination.current = 1; refresh(); }
function openDetail(bindingId: string) { router.push({ name: 'strategy-detail', params: { bindingId } }); }
function shortHash(value?: string) { return value ? value.slice(0, 12) : '-'; }
function formatTime(value?: string) { return value ? new Date(value).toLocaleString() : '-'; }
function modeColor(value?: string) { return value === 'live' ? 'red' : value === 'paper' ? 'orange' : 'blue'; }

onMounted(() => { refresh(); store.startPolling(refresh); });
onUnmounted(() => store.stopPolling());
</script>

<style scoped>
.strategy-page { min-height: 100%; background: var(--color-fill-2); }
.summary-grid { margin-bottom: var(--moox-space-2); }
.summary-card, .table-card { border-radius: 6px; }
.filters { display: flex; flex-wrap: wrap; gap: var(--moox-space-2); margin-bottom: var(--moox-space-2); }
.top-alert { margin-bottom: var(--moox-space-2); }
</style>
