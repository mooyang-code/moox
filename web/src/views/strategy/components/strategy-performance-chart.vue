<template>
  <div ref="root" class="performance-chart">
    <a-empty v-if="!points.length" description="暂无绩效数据" />
  </div>
</template>

<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref, watch } from 'vue';
import VChart from '@visactor/vchart';
import type { PerformancePoint } from '@/api/strategy-types';

const props = defineProps<{ points: PerformancePoint[] }>();
const root = ref<HTMLElement>();
let chart: VChart | null = null;
function release() { chart?.release(); chart = null; }
async function render() {
  await nextTick();
  release();
  if (!root.value || !props.points.length) return;
  const values = props.points.map((point) => ({ time: point.point_time, nav: Number(point.nav), drawdown: Number(point.drawdown) }));
  chart = new VChart({
    type: 'common',
    series: [
      { type: 'line', xField: 'time', yField: 'value', seriesField: 'metric', data: values.map((point) => ({ time: point.time, metric: 'NAV', value: point.nav })) },
      { type: 'line', xField: 'time', yField: 'value', seriesField: 'metric', data: values.map((point) => ({ time: point.time, metric: '回撤', value: point.drawdown })) },
    ],
    axes: [{ orient: 'left' }, { orient: 'bottom', sampling: true }],
    legends: { visible: true, orient: 'top' },
    tooltip: { mark: { visible: true } },
  } as any, { dom: root.value });
  chart.renderSync();
}
watch(() => props.points, render, { deep: true });
onBeforeUnmount(release);
</script>

<style scoped>
.performance-chart { min-height: 320px; width: 100%; }
</style>
