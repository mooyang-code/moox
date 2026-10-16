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
      <span class="arrival-scale" role="group" aria-label="时间范围">
        <button type="button" :class="{ active: scale === 'window' }" @click="scale = 'window'">3 小时</button>
        <button type="button" :class="{ active: scale === 'day' }" @click="scale = 'day'">全天</button>
      </span>
      <span v-if="scale === 'window' && model.series.length" class="arrival-pan">
        <button type="button" aria-label="早 1 小时" :disabled="!canShiftEarlier" @click="shift(-1)">‹ 早 1 小时</button>
        <span class="arrival-range">{{ rangeText }}</span>
        <button type="button" aria-label="晚 1 小时" :disabled="!canShiftLater" @click="shift(1)">晚 1 小时 ›</button>
        <button v-if="pinnedWindow" type="button" class="arrival-reset" @click="pinnedWindow = null">回到默认</button>
      </span>
      <span v-if="loading" class="muted">加载中…</span>
      <span v-else-if="error" class="arrival-error">{{ error }}</span>
      <span v-else-if="model.notice" class="muted">{{ model.notice }}</span>
    </div>
    <p v-if="!chains.length" class="muted">没有可查看的数据链路（入库环节还没有行情数据集）</p>
    <div v-show="model.series.length" ref="chartRef" class="arrival-canvas" />
    <ul v-if="model.series.length" class="arrival-summary">
      <li v-for="series in model.series" :key="series.key" :class="{ 'is-bad': series.lateCount }">
        <strong>{{ series.name }}</strong
        ><span>全天 {{ series.summary }}</span>
      </li>
    </ul>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onUnmounted, ref, watch } from "vue";
import { default as VChart } from "@visactor/vchart";
import { monitorApi, type ArrivalHistory, type HealthOverview } from "@/api/monitor";
import type { NameBook } from "@/views/home/home-model";
import {
  arrivalChains,
  arrivalChartSpec,
  arrivalModel,
  canShiftArrivalWindow,
  defaultArrivalWindow,
  pipelineDisplayNames,
  shiftArrivalWindow,
  visibleArrivalWindow,
  WINDOW_STEP_MS,
  type ArrivalWindow
} from "./arrival-model";
import { marketClock, marketDay, zoneLabel } from "./market-time";

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
// 时间范围：默认 3 小时窗口（看今天时为当前时间前 2 小时到后 1 小时，跨天以当天界限为准），可切到全天；
// pinnedWindow 是手动前后挪动后的窗口，为空时跟随默认窗口（看今天时随页面刷新向后走）。窗口不截断，显示时截到当天。
const scale = ref<"window" | "day">("window");
const pinnedWindow = ref<ArrivalWindow | null>(null);
const logicalWindow = computed<ArrivalWindow | undefined>(() => {
  if (scale.value === "day" || !model.value.series.length) return undefined;
  return pinnedWindow.value || defaultArrivalWindow(model.value, props.nowMs);
});
const visibleWindow = computed(() => logicalWindow.value && visibleArrivalWindow(model.value, logicalWindow.value));
const shiftable = computed(() => logicalWindow.value && canShiftArrivalWindow(model.value, logicalWindow.value));
const canShiftEarlier = computed(() => !!shiftable.value?.earlier);
const canShiftLater = computed(() => !!shiftable.value?.later);
const rangeText = computed(() => {
  const window = visibleWindow.value;
  if (!window) return "";
  const end = window.endMs >= model.value.endMs ? "24:00" : marketClock(window.endMs, model.value.timezone);
  return `${marketClock(window.startMs, model.value.timezone)}–${end} ${model.value.zone}`;
});
function shift(direction: number) {
  if (!logicalWindow.value) return;
  pinnedWindow.value = shiftArrivalWindow(model.value, logicalWindow.value, direction * WINDOW_STEP_MS);
}
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
  chart = new VChart(arrivalChartSpec(model.value, props.nowMs, visibleWindow.value) as never, { dom: chartRef.value });
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

watch(
  [chainKey, day],
  () => {
    pinnedWindow.value = null;
    void load();
  },
  { immediate: true }
);
// 切换范围或挪动窗口只重画，不重新查询。
watch([scale, pinnedWindow], () => render());
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
.arrival-scale,
.arrival-pan {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
.arrival-scale {
  gap: 0;
  overflow: hidden;
  border: 1px solid var(--color-border-2);
  border-radius: 4px;
}
.arrival-scale button,
.arrival-pan button {
  padding: 1px 10px;
  border: 0;
  background: none;
  color: var(--color-text-2);
  font-size: 12px;
  cursor: pointer;
}
.arrival-scale button + button {
  border-left: 1px solid var(--color-border-2);
}
.arrival-scale button.active {
  background: rgb(var(--primary-1));
  color: rgb(var(--primary-6));
}
.arrival-pan button {
  padding: 1px 4px;
  color: rgb(var(--primary-6));
}
.arrival-pan button:disabled {
  color: var(--color-text-4);
  cursor: default;
}
.arrival-range {
  color: var(--color-text-1);
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
