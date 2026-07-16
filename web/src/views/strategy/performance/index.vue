<template>
  <a-card title="策略表现" :bordered="false">
    <div class="performance-toolbar">
      <a-radio-group v-model="source" type="button" @change="load"><a-radio v-for="item in sourceOptions" :key="item" :value="item">{{ item }}</a-radio></a-radio-group>
      <span class="as-of">数据时间：{{ performance?.as_of || '-' }}</span>
    </div>
    <a-alert v-if="performance?.summary?.status === 'insufficient_data'" type="warning" show-icon>数据不足，暂不计算绩效。</a-alert>
    <a-alert v-if="error" type="error" show-icon>{{ error }}</a-alert>
    <a-spin :loading="loading" style="display: block">
    <a-grid :cols="5" :col-gap="12" class="metrics">
      <a-statistic title="净值" :value="performance?.summary?.nav || '-'" />
      <a-statistic title="累计收益" :value="percent(performance?.summary?.return_value)" />
      <a-statistic title="最大回撤" :value="percent(performance?.summary?.max_drawdown)" />
      <a-statistic title="换手" :value="performance?.summary?.turnover || '-'" />
      <a-statistic title="费用" :value="performance?.summary?.fees || '-'" />
    </a-grid>
    <PerformanceChart :points="performance?.points || []" />
    </a-spin>
  </a-card>
</template>

<script setup lang="ts">
import { onMounted, ref, watch } from 'vue';
import { getStrategyPerformance } from '@/api/strategy';
import type { PerformanceSource, StrategyPerformance } from '@/api/strategy-types';
import PerformanceChart from '@/views/strategy/components/strategy-performance-chart.vue';

const props = defineProps<{ bindingId: string }>();
const source = ref<PerformanceSource>('paper');
const performance = ref<StrategyPerformance | null>(null);
const loading = ref(false);
const error = ref('');
const sourceOptions = ['backtest', 'observe', 'paper', 'live'];
async function load() {
  loading.value = true;
  error.value = '';
  try {
    performance.value = await getStrategyPerformance(props.bindingId, source.value);
  } catch (cause) {
    performance.value = null;
    error.value = cause instanceof Error ? cause.message : '绩效数据加载失败';
  } finally {
    loading.value = false;
  }
}
function percent(value?: string) { return value ? `${(Number(value) * 100).toFixed(2)}%` : '-'; }
watch(() => props.bindingId, load);
onMounted(load);
</script>

<style scoped>
.performance-toolbar { display: flex; align-items: center; justify-content: space-between; margin-bottom: 8px; }
.as-of { color: var(--color-text-3); font-size: 12px; }
.metrics { margin: 8px 0; }
</style>
