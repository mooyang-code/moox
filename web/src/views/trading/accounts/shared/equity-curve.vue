<template>
  <div class="equity-curve">
    <a-radio-group v-model="rangeDays" type="button" size="mini" class="range" @change="load">
      <a-radio :value="7">7 天</a-radio>
      <a-radio :value="30">30 天</a-radio>
    </a-radio-group>
    <div ref="container" class="chart" />
    <a-empty v-if="!loading && !points.length" description="暂无资金曲线" />
  </div>
</template>

<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref, watch } from "vue";
import { default as VChart } from "@visactor/vchart";
import { queryEquityCurve, type EquityPoint } from "@/api/trade";
import { equityRange, toEquitySeries } from "./equity-curve";

const props = defineProps<{ logicalAccountId: string }>();
const container = ref<HTMLElement>();
const points = ref<EquityPoint[]>([]);
const loading = ref(false);
const rangeDays = ref(30);
let chart: VChart | null = null;
let requestSeq = 0;

async function load() {
  const seq = ++requestSeq;
  const logicalAccountId = props.logicalAccountId;
  loading.value = true;
  points.value = [];
  chart?.release();
  chart = null;
  try {
    const response = await queryEquityCurve({
      logical_account_id: logicalAccountId,
      ...equityRange(rangeDays.value, Date.now())
    });
    if (seq !== requestSeq || logicalAccountId !== props.logicalAccountId) return;
    points.value = response.points || [];
    await nextTick();
    if (seq !== requestSeq || logicalAccountId !== props.logicalAccountId || !container.value) return;
    chart = new VChart(
      {
        type: "line",
        data: [{ id: "equity", values: toEquitySeries(points.value) }],
        xField: "time",
        yField: "value",
        seriesField: undefined,
        axes: [
          { orient: "bottom", type: "time" },
          { orient: "left", type: "linear" }
        ],
        line: { style: { lineWidth: 2 } },
        point: { visible: false }
      },
      { dom: container.value }
    );
    chart.renderSync();
  } finally {
    if (seq === requestSeq) loading.value = false;
  }
}

watch(() => props.logicalAccountId, load, { immediate: true });
onBeforeUnmount(() => chart?.release());
</script>

<style scoped>
.range {
  margin-bottom: 8px;
}
.chart {
  min-height: 260px;
  width: 100%;
}
</style>
