<template>
  <div class="equity-curve">
    <a-radio-group v-model="rangeDays" type="button" size="mini" class="range" @change="load">
      <a-radio :value="7">7 天</a-radio>
      <a-radio :value="30">30 天</a-radio>
    </a-radio-group>
    <!-- 读取中、失败、没有数据三种状态各占图表的位置，不和空白画布叠在一起 -->
    <div v-show="!loading && !error && points.length" ref="container" class="chart" />
    <div v-if="loading" class="chart state"><a-spin /></div>
    <div v-else-if="error" class="chart state">
      <span class="tx-muted">资金曲线读取失败：{{ error }}</span>
      <a-button size="mini" @click="load">重试</a-button>
    </div>
    <a-empty v-else-if="!points.length" class="chart state" description="这段时间没有资金曲线" />
  </div>
</template>

<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref, watch } from "vue";
import { default as VChart } from "@visactor/vchart";
import { queryEquityCurve, type EquityPoint } from "@/api/trade";
import { equityChartParts, equityRange, toEquitySeries } from "./equity-curve";
import { useDisplayZone } from "@/hooks/useDisplayZone";

const props = defineProps<{ logicalAccountId: string }>();
// 横轴和提示框按当前空间登记的时区
const displayZone = useDisplayZone();
const container = ref<HTMLElement>();
const points = ref<EquityPoint[]>([]);
const loading = ref(false);
const error = ref("");
const rangeDays = ref(30);
let chart: VChart | null = null;
let requestSeq = 0;

async function load() {
  const seq = ++requestSeq;
  const logicalAccountId = props.logicalAccountId;
  loading.value = true;
  error.value = "";
  points.value = [];
  chart?.release();
  chart = null;
  try {
    const response = await queryEquityCurve(
      {
        logical_account_id: logicalAccountId,
        ...equityRange(rangeDays.value, Date.now())
      },
      { silent: true }
    );
    if (seq !== requestSeq || logicalAccountId !== props.logicalAccountId) return;
    points.value = response.points || [];
    // 先结束读取态，图表容器显示出来后再画，否则画布量不到宽度
    loading.value = false;
    await nextTick();
    if (seq !== requestSeq || logicalAccountId !== props.logicalAccountId || !container.value) return;
    const series = toEquitySeries(points.value);
    chart = new VChart(
      {
        type: "line",
        data: [{ id: "equity", values: series }],
        xField: "time",
        yField: "value",
        seriesField: undefined,
        ...equityChartParts(series, displayZone.value),
        line: { style: { lineWidth: 2 } },
        point: { visible: false }
      },
      { dom: container.value }
    );
    chart.renderSync();
  } catch (reason) {
    if (seq === requestSeq) error.value = reason instanceof Error ? reason.message : "请稍后重试";
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
.state {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
}
</style>
