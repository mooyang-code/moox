<template>
  <div class="metric-chart" ref="chartRoot">
    <div v-if="loading" class="chart-state"><a-spin /></div>
    <a-empty v-else-if="!series.length" description="暂无历史数据" />
  </div>
</template>

<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref, watch } from "vue";
import VChart from "@visactor/vchart";

export interface ChartPoint {
  time: string;
  value: number;
  series: string;
}

const props = defineProps<{
  series: ChartPoint[];
  loading?: boolean;
}>();

const chartRoot = ref<HTMLElement>();
let chart: VChart | null = null;

function releaseChart() {
  chart?.release();
  chart = null;
}

async function renderChart() {
  await nextTick();
  releaseChart();
  if (!chartRoot.value || props.loading || !props.series.length) return;
  const spec = {
    type: "line",
    data: [{ id: "metric-history", values: props.series }],
    xField: "time",
    yField: "value",
    seriesField: "series",
    line: { style: { lineWidth: 2, curveType: "monotone" } },
    point: { visible: false },
    legends: { visible: true, orient: "top" },
    axes: [
      { orient: "left", title: { visible: true, text: "值" } },
      { orient: "bottom", sampling: true, label: { style: { fontSize: 10 } } }
    ],
    tooltip: { mark: { content: [{ key: (d: ChartPoint) => d.series, value: (d: ChartPoint) => d.value }] } },
    crosshair: { xField: { visible: true } }
  };
  chart = new VChart(spec as any, { dom: chartRoot.value });
  chart.renderSync();
}

watch(() => [props.series, props.loading], renderChart, { deep: true });
onBeforeUnmount(releaseChart);
</script>

<style scoped lang="scss">
.metric-chart {
  min-height: 320px;
  width: 100%;
  position: relative;
}

.chart-state {
  min-height: 320px;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: var(--moox-space-3);
  color: var(--color-text-3);
}
</style>
