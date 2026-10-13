<template>
  <div v-if="results.length" class="overview">
    <div class="stats">
      <div class="stat">
        <span class="label">本页期数</span><strong>{{ overview.total }}</strong>
      </div>
      <div class="stat">
        <span class="label">成功 / 跳过</span><strong>{{ overview.ok }} / {{ overview.skipped }}</strong>
      </div>
      <div class="stat">
        <span class="label">产出延迟（平均 / 最大）</span
        ><strong>{{ formatDelay(overview.avgDelayMs) }} / {{ formatDelay(overview.maxDelayMs) }}</strong>
      </div>
      <div class="stat">
        <span class="label">跳过原因</span>
        <span v-if="!overview.skipReasons.length" class="muted">无</span>
        <a-space v-else wrap size="mini"
          ><a-tag v-for="item in overview.skipReasons" :key="item.reason" size="small" color="orange"
            >{{ skipReasonLabel(item.reason) }} × {{ item.count }}</a-tag
          ></a-space
        >
      </div>
    </div>
    <div v-if="points.length > 1" class="trend">
      <svg viewBox="0 0 400 60" preserveAspectRatio="none" role="img" aria-label="目标数与换手趋势">
        <polyline class="line targets" :points="targetsLine" />
        <polyline class="line turnover" :points="turnoverLine" />
      </svg>
      <div class="legend">
        <span class="targets">● 目标数（0–{{ maxTargets }}）</span>
        <span class="turnover">● 换手（0–{{ percent(maxTurnover) }}）</span>
        <span class="muted"
          >{{ formatStrategyTime(points[0].time) }} → {{ formatStrategyTime(points[points.length - 1].time) }}</span
        >
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { StrategyResult } from "@/api/strategy-types";
import { buildResultOverview, formatDelay, formatStrategyTime, percent, skipReasonLabel } from "@/views/strategy/model";

const props = defineProps<{ results: StrategyResult[] }>();
const overview = computed(() => buildResultOverview(props.results));
const points = computed(() => overview.value.trend);
const maxTargets = computed(() => Math.max(1, ...points.value.map(point => point.targets)));
const maxTurnover = computed(() => Math.max(0.0001, ...points.value.map(point => point.turnover)));

function line(values: number[], max: number): string {
  const step = values.length > 1 ? 400 / (values.length - 1) : 0;
  return values.map((value, index) => `${(index * step).toFixed(1)},${(56 - (value / max) * 52).toFixed(1)}`).join(" ");
}
const targetsLine = computed(() =>
  line(
    points.value.map(point => point.targets),
    maxTargets.value
  )
);
const turnoverLine = computed(() =>
  line(
    points.value.map(point => point.turnover),
    maxTurnover.value
  )
);
</script>

<style scoped>
.overview {
  display: grid;
  gap: 10px;
  margin-bottom: 12px;
}
.stats {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  gap: 8px;
}
.stat {
  display: grid;
  gap: 4px;
  padding: 8px 10px;
  background: var(--color-fill-2);
}
.label,
.muted {
  color: var(--color-text-3);
  font-size: 12px;
}
.trend svg {
  width: 100%;
  height: 60px;
  background: var(--color-fill-2);
}
.line {
  fill: none;
  stroke-width: 1.5;
  vector-effect: non-scaling-stroke;
}
.line.targets,
.legend .targets {
  stroke: rgb(var(--arcoblue-6));
  color: rgb(var(--arcoblue-6));
}
.line.turnover,
.legend .turnover {
  stroke: rgb(var(--orange-6));
  color: rgb(var(--orange-6));
}
.legend {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 16px;
  margin-top: 4px;
  font-size: 12px;
}
</style>
