<template>
  <div class="arrival-chart">
    <div class="arrival-controls">
      <label>
        <span>链路</span>
        <select v-model="chainKey" aria-label="链路" :disabled="!chains.length">
          <option v-for="chain in chains" :key="chain.key" :value="chain.key">{{ chain.label }}</option>
        </select>
      </label>
      <label>
        <span>日期（{{ zone }}）</span>
        <input v-model="day" type="date" aria-label="日期" :min="minDay" :max="maxDay" />
      </label>
      <span v-if="loading" class="muted">加载中…</span>
      <span v-else-if="error" class="arrival-error">{{ error }}</span>
      <span v-else-if="model.notice" class="muted">{{ model.notice }}</span>
    </div>
    <p v-if="!chains.length" class="muted">没有可查看的数据链路（入库环节还没有行情数据集）</p>
    <div v-show="model.series.length" ref="chartRef" class="arrival-canvas" />
    <ul v-if="model.series.length" class="arrival-summary">
      <li v-for="series in model.series" :key="series.key" :class="{ 'is-bad': series.lateCount }">
        <strong>{{ series.name }}</strong
        ><span>{{ series.summary }}</span>
      </li>
    </ul>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onUnmounted, ref, watch } from "vue";
import { default as VChart } from "@visactor/vchart";
import { monitorApi, type ArrivalHistory, type HealthOverview } from "@/api/monitor";
import type { NameBook } from "@/views/home/home-model";
import { arrivalChains, arrivalChartSpec, arrivalModel, pipelineDisplayNames } from "./arrival-model";
import { marketDay, zoneLabel } from "./market-time";

const props = defineProps<{ overview?: HealthOverview; names: NameBook; nowMs: number }>();

const DAY_MS = 24 * 60 * 60 * 1000;
const chains = computed(() => arrivalChains(props.overview, props.names));
const chainKey = ref("");
const chain = computed(() => chains.value.find(item => item.key === chainKey.value));
const zone = computed(() => zoneLabel(chain.value?.timezone, props.nowMs));
// 到达记录保留 7 天：可选今天及之前 7 天（按市场时区）。
const maxDay = computed(() => marketDay(props.nowMs, chain.value?.timezone));
const minDay = computed(() => marketDay(props.nowMs - 7 * DAY_MS, chain.value?.timezone));
const day = ref("");
const history = ref<ArrivalHistory>();
const loading = ref(false);
const error = ref("");
const model = computed(() => arrivalModel(history.value, props.names, pipelineDisplayNames(props.overview)));
const chartRef = ref<HTMLElement>();
let chart: VChart | null = null;
let requestID = 0;

watch(
  chains,
  list => {
    if (!list.some(item => item.key === chainKey.value)) chainKey.value = list[0]?.key || "";
  },
  { immediate: true }
);
// 换链路时市场时区可能不同（加密货币 UTC、A 股 UTC+8），日期回到那个时区的今天。
watch(
  () => chain.value?.timezone,
  () => {
    day.value = maxDay.value;
  },
  { immediate: true }
);
// 跨过市场时区的 0 点：正在看“今天”的跟到新的一天。
watch(maxDay, (next, previous) => {
  if (day.value === previous) day.value = next;
});

function render() {
  chart?.release();
  chart = null;
  if (!chartRef.value || !model.value.series.length) return;
  chart = new VChart(arrivalChartSpec(model.value, props.nowMs) as never, { dom: chartRef.value });
  chart.renderSync();
}

/** refresh 为 true 时是页面定时刷新：保留当前曲线，取回后再替换，不闪烁也不显示加载中。 */
async function load(options: { refresh?: boolean } = {}) {
  const current = chain.value;
  const id = ++requestID;
  if (!options.refresh) history.value = undefined;
  error.value = "";
  if (!current || !day.value) {
    history.value = undefined;
    render();
    return;
  }
  loading.value = !options.refresh;
  try {
    const rsp = await monitorApi.queryArrivalHistory({
      space_id: current.spaceId,
      dataset_id: current.datasetId,
      freq: current.freq,
      day: day.value
    });
    if (id !== requestID) return;
    history.value = rsp;
  } catch (err) {
    if (id === requestID) error.value = err instanceof Error ? err.message : "到达历史加载失败";
  } finally {
    if (id === requestID) loading.value = false;
  }
  await nextTick();
  if (id === requestID) render();
}

watch([chainKey, day], () => load(), { immediate: true });
// 页面每次刷新（30 秒轮询或点“刷新”）时，正在看今天就重新加载，曲线跟上新到的 K 线；看历史日期时不重复查询。
watch(
  () => props.nowMs,
  () => {
    if (day.value === maxDay.value) void load({ refresh: true });
  }
);
onUnmounted(() => {
  requestID++;
  chart?.release();
});
</script>

<style scoped lang="scss">
.arrival-chart {
  margin-top: 10px;
}
.arrival-controls {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 12px;
  font-size: 13px;
}
.arrival-controls label {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: var(--color-text-3);
}
.arrival-controls select,
.arrival-controls input {
  height: 28px;
  padding: 0 8px;
  border: 1px solid var(--color-border-2);
  border-radius: 4px;
  background: var(--color-bg-2);
  color: var(--color-text-1);
}
.muted {
  color: var(--color-text-3);
}
.arrival-error {
  color: rgb(var(--red-6));
}
.arrival-canvas {
  width: 100%;
  height: 320px;
  margin-top: 10px;
}
.arrival-summary {
  display: grid;
  gap: 4px;
  margin: 8px 0 0;
  padding: 0;
  list-style: none;
  font-size: 12px;
  color: var(--color-text-3);
}
.arrival-summary strong {
  margin-right: 8px;
  color: var(--color-text-1);
  font-weight: 400;
}
.arrival-summary .is-bad strong {
  color: rgb(var(--red-6));
}
</style>
