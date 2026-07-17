<template>
  <a-modal v-model:visible="visibleModel" :title="title" width="1080px" :footer="false">
    <a-spin class="kline-spin" :loading="loading">
      <div class="kline-modal-body">
        <div class="kline-toolbar">
          <div class="kline-symbol-block">
            <strong>{{ subjectId }}</strong>
            <span>{{ freq }}</span>
            <span>{{ records.length }} 根</span>
          </div>
          <div class="kline-control-strip">
            <a-tooltip :content="playing ? '停止播放' : '播放K线'">
              <a-button
                class="kline-play-button"
                size="small"
                type="outline"
                shape="circle"
                :disabled="records.length === 0"
                @click="togglePlayback"
              >
                <template #icon>
                  <icon-pause v-if="playing" />
                  <icon-play-arrow v-else />
                </template>
              </a-button>
            </a-tooltip>
            <span>数量</span>
            <a-input-number
              v-model="limitModel"
              class="kline-limit-input"
              size="small"
              :min="MIN_KLINE_LIMIT"
              :max="MAX_KLINE_LIMIT"
              :step="50"
              :precision="0"
            />
            <a-button size="small" type="outline" :loading="loading" @click="emit('reload')">
              <template #icon><icon-refresh /></template>
              应用
            </a-button>
          </div>
          <div v-if="latest" class="kline-price-strip">
            <span class="kline-last-price" :class="changeClass">{{ formatKlineNumber(latest.close) }}</span>
            <span :class="changeClass">{{ changeText }}</span>
          </div>
        </div>
        <div v-if="records.length > 0" ref="chartHost" class="kline-chart-host"></div>
        <a-empty v-else description="当前结果缺少 open/high/low/close 字段，无法生成K线图" />
      </div>
    </a-spin>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { TooltipShowRule, TooltipShowType, dispose as disposeChartInstance, init as initChart } from "klinecharts";
import type { Chart } from "klinecharts";
import { MAX_KLINE_LIMIT, MIN_KLINE_LIMIT, type KlineChartRecord } from "./view-browse-utils";

const props = defineProps<{
  visible: boolean;
  subjectId: string;
  freq: string;
  records: KlineChartRecord[];
  loading: boolean;
  limit: number;
}>();

const emit = defineEmits<{
  (event: "update:visible", value: boolean): void;
  (event: "update:limit", value: number): void;
  (event: "reload"): void;
}>();

const visibleModel = computed({ get: () => props.visible, set: value => emit("update:visible", value) });
const limitModel = computed({ get: () => props.limit, set: value => emit("update:limit", value) });
const title = computed(() => (props.subjectId ? `${props.subjectId} K线` : "K线图"));
const latest = computed(() => props.records[props.records.length - 1]);
const previous = computed(() => props.records[props.records.length - 2]);
const priceChange = computed(() => (latest.value && previous.value ? latest.value.close - previous.value.close : 0));
const priceChangePercent = computed(() => (previous.value?.close ? (priceChange.value / previous.value.close) * 100 : 0));
const changeClass = computed(() => (priceChange.value > 0 ? "is-up" : priceChange.value < 0 ? "is-down" : "is-flat"));
const changeText = computed(() => {
  const sign = priceChange.value > 0 ? "+" : "";
  return `${sign}${formatKlineNumber(priceChange.value)} (${sign}${priceChangePercent.value.toFixed(2)}%)`;
});

const chartHost = ref<HTMLElement>();
const playing = ref(false);
const playbackCursor = ref(0);
let chart: Chart | null = null;
let resizeObserver: ResizeObserver | null = null;
let playbackTimer: ReturnType<typeof setInterval> | null = null;
const PLAYBACK_INTERVAL_MS = 320;

function renderChart() {
  const host = chartHost.value;
  if (!props.visible || !host || props.records.length === 0) return;
  disposeChart();
  chart = initChart(host, {
    locale: "zh-CN",
    timezone: "Asia/Shanghai",
    styles: {
      grid: {
        horizontal: { color: "rgba(160, 174, 192, 0.12)" },
        vertical: { color: "rgba(160, 174, 192, 0.12)" }
      },
      candle: {
        bar: {
          upColor: "#ef5350",
          downColor: "#26a69a",
          noChangeColor: "#9ba8b7",
          upBorderColor: "#ef5350",
          downBorderColor: "#26a69a",
          noChangeBorderColor: "#9ba8b7",
          upWickColor: "#ef5350",
          downWickColor: "#26a69a",
          noChangeWickColor: "#9ba8b7"
        },
        tooltip: { showRule: TooltipShowRule.None, showType: TooltipShowType.Standard, text: { color: "#d9e1ec", size: 12 } }
      },
      indicator: {
        tooltip: { showRule: TooltipShowRule.None, showName: true, showParams: true, text: { color: "#9ba8b7", size: 12 } }
      },
      xAxis: { axisLine: { color: "rgba(160, 174, 192, 0.22)" }, tickText: { color: "#8c9bab" } },
      yAxis: { axisLine: { color: "rgba(160, 174, 192, 0.22)" }, tickText: { color: "#8c9bab" } },
      separator: { color: "#222b35" },
      crosshair: {
        horizontal: { line: { color: "rgba(217, 225, 236, 0.45)" }, text: { backgroundColor: "#263241" } },
        vertical: { line: { color: "rgba(217, 225, 236, 0.45)" }, text: { backgroundColor: "#263241" } }
      }
    }
  });
  if (!chart) {
    Message.error("K线图初始化失败");
    return;
  }
  chart.setPriceVolumePrecision(8, 2);
  chart.setBarSpace(10);
  chart.applyNewData(props.records);
  chart.createIndicator("VOL", false, { height: 112, minHeight: 84 });
  playbackCursor.value = props.records.length;
  resizeObserver = new ResizeObserver(([entry]) => {
    if (chart && entry.contentRect.width > 0 && entry.contentRect.height > 0) chart.resize();
  });
  resizeObserver.observe(host);
}

function togglePlayback() {
  if (playing.value) {
    stopPlayback();
    return;
  }
  if (!chart || props.records.length === 0) return;
  stopPlayback();
  if (playbackCursor.value <= 0 || playbackCursor.value >= props.records.length) playbackCursor.value = 1;
  playing.value = true;
  applyPlaybackFrame();
  playbackTimer = setInterval(() => {
    if (!playing.value || playbackCursor.value >= props.records.length) {
      stopPlayback();
      return;
    }
    playbackCursor.value += 1;
    applyPlaybackFrame();
    if (playbackCursor.value >= props.records.length) stopPlayback();
  }, PLAYBACK_INTERVAL_MS);
}

function applyPlaybackFrame() {
  if (!chart || props.records.length === 0) return;
  const cursor = Math.min(props.records.length, Math.max(1, playbackCursor.value));
  chart.applyNewData(props.records.slice(0, cursor));
}

function stopPlayback() {
  if (playbackTimer) clearInterval(playbackTimer);
  playbackTimer = null;
  playing.value = false;
}

function disposeChart() {
  stopPlayback();
  resizeObserver?.disconnect();
  resizeObserver = null;
  if (chart) disposeChartInstance(chart);
  chart = null;
}

function formatKlineNumber(value: number) {
  return Number.isFinite(value) ? value.toLocaleString(undefined, { maximumFractionDigits: 8 }) : "-";
}

watch(
  () => [props.visible, props.records] as const,
  ([visible]) => {
    if (visible) nextTick(renderChart);
    else disposeChart();
  },
  { deep: true }
);
onBeforeUnmount(disposeChart);
</script>

<style scoped>
.kline-modal-body {
  display: flex;
  flex-direction: column;
  gap: var(--moox-space-3);
  width: 100%;
  min-width: 0;
  box-sizing: border-box;
  overflow: hidden;
}
.kline-spin {
  display: block;
  width: 100%;
  min-width: 0;
}
.kline-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--moox-space-4);
  min-height: 48px;
  width: 100%;
  padding: 10px 14px;
  border: 1px solid #222b35;
  border-radius: 8px;
  box-sizing: border-box;
  color: #d9e1ec;
  background: #151a21;
  overflow: hidden;
}
.kline-symbol-block,
.kline-control-strip,
.kline-price-strip {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 10px;
  min-width: 0;
}
.kline-symbol-block strong {
  color: #fff;
  font-size: 18px;
  font-weight: 700;
}
.kline-symbol-block span,
.kline-control-strip {
  color: #9ba8b7;
  font-size: 12px;
}
.kline-control-strip {
  justify-content: center;
}
.kline-control-strip :deep(.arco-btn-outline) {
  border-color: #2f3b4a;
  color: #d9e1ec;
  background: transparent;
}
.kline-play-button {
  flex: 0 0 auto;
}
.kline-play-button:not(:disabled):hover {
  border-color: #26a69a;
  color: #26a69a;
}
.kline-limit-input {
  width: 108px;
}
.kline-limit-input :deep(.arco-input-wrapper) {
  border-color: #2f3b4a;
  color: #d9e1ec;
  background: #101318;
}
.kline-price-strip {
  justify-content: flex-end;
  font-weight: 600;
}
.kline-last-price {
  font-size: 20px;
}
.is-up {
  color: #ef5350;
}
.is-down {
  color: #26a69a;
}
.is-flat {
  color: #9ba8b7;
}
.kline-chart-host {
  width: 100%;
  max-width: 100%;
  height: min(62vh, 560px);
  min-height: 420px;
  box-sizing: border-box;
  overflow: hidden;
  border: 1px solid #222b35;
  border-radius: 8px;
  background: #101318;
}
@media (max-width: 560px) {
  .kline-toolbar {
    align-items: flex-start;
    flex-direction: column;
  }
  .kline-price-strip,
  .kline-control-strip {
    justify-content: flex-start;
  }
  .kline-chart-host {
    min-height: 360px;
  }
}
</style>
