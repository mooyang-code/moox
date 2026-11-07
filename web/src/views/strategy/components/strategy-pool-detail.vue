<template>
  <section class="pool" aria-label="策略池">
    <header class="pool-head">
      <strong>策略池</strong>
      <span class="muted">{{ headText }}</span>
    </header>
    <p class="pool-note">
      成员净值是按收盘价成交的模拟账本曲线（含手续费，未计资金费与强平），只用来分配资金，不代表实盘收益。图表只覆盖当前已加载的结果页（共
      {{ periods.length }} 期，{{ rangeText }}）；横轴按期排列，不按时间等距，不同会话之间的曲线不连接。
    </p>

    <ul class="legend" aria-label="成员图例">
      <li v-for="(id, index) in ids" :key="id" class="legend-item">
        <span class="swatch" :style="{ background: memberColor(index) }" aria-hidden="true" />
        <span class="mono">{{ id }}</span>
        <span v-if="bankrupt.has(id)" class="bankrupt">爆仓</span>
      </li>
    </ul>

    <div class="chart-block">
      <div class="chart-title">成员净值<span class="muted">初始为 1，虚线是 1</span></div>
      <div class="chart-body">
        <div class="axis">
          <span>{{ equityTop }}</span>
          <span>{{ equityBottom }}</span>
        </div>
        <svg class="chart" :viewBox="`0 0 ${W} ${H}`" preserveAspectRatio="none" role="img" aria-label="成员净值曲线">
          <line class="baseline" x1="0" x2="600" :y1="equityY(1)" :y2="equityY(1)" />
          <template v-for="(item, index) in equity" :key="item.id">
            <polyline
              v-for="(segment, segmentIndex) in lineSegments(item.values, breaks)"
              :key="segmentIndex"
              class="line"
              :style="{ stroke: memberColor(index) }"
              :points="linePoints(item.values, segment, equityY)"
            />
          </template>
          <line v-if="selectedX !== null" class="marker" :x1="selectedX" :x2="selectedX" y1="0" :y2="H" />
        </svg>
      </div>
    </div>

    <div class="chart-block">
      <div class="chart-title">每期分配比例<span class="muted">步长限制之后，按成员堆叠</span></div>
      <div class="chart-body">
        <div class="axis">
          <span>{{ percent(ratioMax, 0) }}</span>
          <span>0%</span>
        </div>
        <svg class="chart" :viewBox="`0 0 ${W} ${H}`" preserveAspectRatio="none" role="img" aria-label="每期分配比例堆叠面积图">
          <polygon
            v-for="(area, index) in areas"
            :key="ids[index]"
            class="area"
            :style="{ fill: memberColor(index) }"
            :points="area"
          />
          <line v-if="selectedX !== null" class="marker" :x1="selectedX" :x2="selectedX" y1="0" :y2="H" />
        </svg>
      </div>
    </div>

    <div v-if="showTiming" class="chart-block">
      <div class="chart-title">择时状态<span class="muted">实色为持有，灰色为空仓</span></div>
      <div v-for="(item, index) in timing" :key="item.id" class="timing-row">
        <span class="mono timing-label">{{ item.id }}</span>
        <svg
          class="timing-strip"
          :viewBox="`0 0 ${periods.length} 1`"
          preserveAspectRatio="none"
          role="img"
          :aria-label="`成员 ${item.id} 的择时状态`"
        >
          <template v-for="(value, position) in item.values" :key="position">
            <rect
              v-if="value !== null"
              :class="value === 1 ? 'hold' : 'flat'"
              :style="value === 1 ? { fill: memberColor(index) } : undefined"
              :x="position"
              y="0"
              width="1"
              height="1"
            />
          </template>
        </svg>
      </div>
    </div>

    <div class="range">
      <span class="muted">{{ formatStrategyTime(periods[0].time) }}</span>
      <span class="muted">{{ formatStrategyTime(periods[periods.length - 1].time) }}</span>
    </div>

    <div class="detail-head">
      <strong>当期明细</strong>
      <a-select :model-value="selected.resultId" class="period-select" size="small" aria-label="选择期" @change="onPick">
        <a-option v-for="period in descending" :key="period.resultId" :value="period.resultId">{{
          periodLabel(period)
        }}</a-option>
      </a-select>
    </div>
    <PoolMembers :period="selected" :ids="ids" :show-timing="showTiming" />
  </section>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import PoolMembers from "@/views/strategy/components/strategy-pool-members.vue";
import { formatStrategyTime, percent } from "@/views/strategy/model";
import {
  bankruptMemberIds,
  equityRange,
  equitySeries,
  lineSegments,
  memberColor,
  poolMemberIds,
  ratioAxisMax,
  ratioSeries,
  sessionBreaks,
  timingSeries,
  type PoolPeriod
} from "@/views/strategy/pool-model";

// 策略池详情：成员净值曲线、每期分配比例、择时状态，以及可选期的成员明细。只读取已加载的结果，不请求新数据。
// 调用方保证 periods 非空且按时间从旧到新（buildPoolPeriods 的输出）。
const props = defineProps<{ periods: PoolPeriod[] }>();

const W = 600;
const H = 120;
const picked = ref("");

const ids = computed(() => poolMemberIds(props.periods));
const descending = computed(() => [...props.periods].reverse());
// 没有手动选择，或选的期已不在已加载的结果里（翻页、刷新）时，跟随最新一期。
const selected = computed(
  () => props.periods.find(item => item.resultId === picked.value) ?? props.periods[props.periods.length - 1]
);
const selectedIndex = computed(() => props.periods.findIndex(item => item.resultId === selected.value.resultId));
const showTiming = computed(() => props.periods.some(period => period.timing));
const bankrupt = computed(() => bankruptMemberIds(props.periods));
const breaks = computed(() => sessionBreaks(props.periods));
const equity = computed(() => equitySeries(props.periods, ids.value));
const ratios = computed(() => ratioSeries(props.periods, ids.value));
const timing = computed(() => timingSeries(props.periods, ids.value));
const ratioMax = computed(() => ratioAxisMax(props.periods));
const range = computed(() => equityRange(equity.value));

const methodText = computed(() => {
  const methods = new Set(props.periods.map(period => period.method).filter(Boolean));
  return [...methods].map(method => (method === "rotation" ? "轮动" : method === "fixed" ? "固定比例" : method)).join("、");
});
const headText = computed(() =>
  [`${ids.value.length} 个成员`, methodText.value, showTiming.value ? "叠加择时" : ""].filter(Boolean).join(" · ")
);
const rangeText = computed(
  () => `${formatStrategyTime(props.periods[0].time)} 至 ${formatStrategyTime(props.periods[props.periods.length - 1].time)}`
);

function xOf(index: number): number {
  return props.periods.length > 1 ? (index * W) / (props.periods.length - 1) : W / 2;
}
const selectedX = computed(() => (props.periods.length > 1 && selectedIndex.value >= 0 ? xOf(selectedIndex.value) : null));

function equityY(value: number): number {
  const { min, max } = range.value;
  return H - ((value - min) / (max - min)) * H;
}
const equityTop = computed(() => range.value.max.toFixed(3));
const equityBottom = computed(() => range.value.min.toFixed(3));

// 只有一期时线段退化成点：画一小段带圆头的横线，看起来就是一个点。
function linePoints(values: (number | null)[], segment: number[], yOf: (value: number) => number): string {
  const points = segment.map(index => `${xOf(index).toFixed(1)},${yOf(values[index] as number).toFixed(1)}`);
  if (points.length === 1) {
    const x = xOf(segment[0]);
    const y = yOf(values[segment[0]] as number).toFixed(1);
    return `${(x - 0.5).toFixed(1)},${y} ${(x + 0.5).toFixed(1)},${y}`;
  }
  return points.join(" ");
}

// 堆叠面积：第 k 个成员的上沿是前 k 个成员比例之和，下沿是前 k-1 个之和。只有一期时铺满整个横轴。
const areas = computed(() => {
  const count = props.periods.length;
  const xs = count > 1 ? props.periods.map((_, index) => xOf(index)) : [0, W];
  const stack = (index: number, member: number) =>
    ratios.value.slice(0, member).reduce((sum, item) => sum + (item.values[index] ?? 0), 0);
  const y = (value: number) => (H - (value / ratioMax.value) * H).toFixed(1);
  return ratios.value.map((_, member) => {
    const columns = count > 1 ? xs.map((_, index) => index) : [0, 0];
    const top = columns.map((index, position) => `${xs[position].toFixed(1)},${y(stack(index, member + 1))}`);
    const bottom = columns.map((index, position) => `${xs[position].toFixed(1)},${y(stack(index, member))}`).reverse();
    return [...top, ...bottom].join(" ");
  });
});

function onPick(value: unknown) {
  picked.value = String(value);
}

function periodLabel(period: PoolPeriod): string {
  return `${formatStrategyTime(period.time)}${period.resultStatus === "ok" ? "" : "（本期跳过）"}`;
}
</script>

<style scoped>
.pool {
  min-width: 0;
  padding: var(--moox-space-3);
  border: 1px solid var(--color-border-2);
  border-radius: 6px;
}
.pool-head {
  display: flex;
  align-items: baseline;
  gap: 10px;
  margin-bottom: 6px;
}
.pool-note {
  margin: 0 0 10px;
  color: var(--color-text-3);
  font-size: 12px;
  line-height: 1.6;
}
.muted {
  color: var(--color-text-3);
  font-size: 12px;
}
.legend {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 16px;
  margin: 0 0 12px;
  padding: 0;
  list-style: none;
  font-size: 13px;
}
.legend-item {
  display: flex;
  align-items: center;
  gap: 6px;
}
.swatch {
  width: 10px;
  height: 10px;
  border-radius: 2px;
}
.mono {
  font-family: var(--font-family-mono, ui-monospace, monospace);
}
.bankrupt {
  color: rgb(var(--red-6));
  font-size: 12px;
}
.chart-block {
  margin-bottom: 12px;
}
.chart-title {
  display: flex;
  align-items: baseline;
  gap: 10px;
  margin-bottom: 4px;
  font-size: 13px;
}
.chart-body {
  display: flex;
  gap: 8px;
}
.axis {
  display: flex;
  flex: none;
  flex-direction: column;
  justify-content: space-between;
  width: 44px;
  color: var(--color-text-3);
  font-size: 11px;
  text-align: right;
  font-variant-numeric: tabular-nums;
}
.chart {
  flex: 1;
  min-width: 0;
  height: 120px;
  background: var(--color-fill-2);
}
.line {
  fill: none;
  stroke-width: 1.5;
  stroke-linecap: round;
  stroke-linejoin: round;
  vector-effect: non-scaling-stroke;
}
.area {
  fill-opacity: 0.8;
  stroke: var(--color-bg-2);
  stroke-width: 0.5;
  vector-effect: non-scaling-stroke;
}
.baseline {
  stroke: var(--color-text-4);
  stroke-width: 1;
  stroke-dasharray: 4 3;
  vector-effect: non-scaling-stroke;
}
.marker {
  stroke: var(--color-text-2);
  stroke-width: 1;
  vector-effect: non-scaling-stroke;
}
.timing-row {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 3px;
}
.timing-label {
  flex: none;
  width: 44px;
  overflow: hidden;
  font-size: 11px;
  text-align: right;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.timing-strip {
  flex: 1;
  min-width: 0;
  height: 12px;
  background: var(--color-fill-2);
}
.timing-strip .flat {
  fill: var(--color-fill-4);
}
.range {
  display: flex;
  justify-content: space-between;
  margin: -6px 0 12px 52px;
}
.detail-head {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 8px;
}
.period-select {
  width: 260px;
}
</style>
