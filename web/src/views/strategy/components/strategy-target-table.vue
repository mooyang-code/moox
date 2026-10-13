<template>
  <section class="target-section" :class="{ 'is-stale': stale }" aria-label="当前目标">
    <header class="target-head">
      <div class="target-title">
        <strong>当前目标</strong>
        <a-tag size="small" :color="tag.color">{{ tag.label }}</a-tag>
        <span class="target-count">{{ targets.length }} 个标的</span>
      </div>
      <div v-if="snapshot && (snapshot.bar_end_time || snapshot.valid_until)" class="target-times">
        <span v-if="snapshot.bar_end_time">K 线结束 {{ formatStrategyTime(snapshot.bar_end_time) }}</span>
        <span v-if="snapshot.valid_until">有效至 {{ formatStrategyTime(snapshot.valid_until) }}</span>
      </div>
    </header>
    <a-alert v-if="notice" :type="notice.type" show-icon class="target-alert">{{ notice.text }}</a-alert>
    <div v-if="rows.length" class="target-rows" role="table" aria-label="目标权重">
      <div class="target-row target-row--head" role="row">
        <span role="columnheader">标的</span>
        <span role="columnheader">目标权重</span>
        <span role="columnheader" class="target-weight-col">权重</span>
        <span role="columnheader">方向</span>
      </div>
      <div v-for="row in rows" :key="row.target.instrument_id" class="target-row" role="row">
        <span role="cell" class="target-id">{{ row.target.instrument_id }}</span>
        <span role="cell" class="target-bar" aria-hidden="true"
          ><span :class="['target-bar__fill', row.weight < 0 ? 'short' : 'long']" :style="{ width: `${row.ratio * 100}%` }"
        /></span>
        <span role="cell" :class="['target-weight-col', row.weight < 0 ? 'short' : 'long']">{{
          targetWeightPercent(row.target)
        }}</span>
        <span role="cell">{{ row.weight < 0 ? "空" : "多" }}</span>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { InstrumentTarget, StrategyTargetSnapshot } from "@/api/strategy-types";
import { formatStrategyTime, rankTargets, targetStateTag, targetWeightPercent, type TargetState } from "@/views/strategy/model";

const props = defineProps<{ targets: InstrumentTarget[]; snapshot: StrategyTargetSnapshot | null; state: TargetState }>();

const rows = computed(() => rankTargets(props.targets));
const tag = computed(() => targetStateTag(props.state));
// 过期、停用、无法确认时目标仍然展示（作为最近一次结果的参考），但整体淡化，并给出说明。
const stale = computed(() => props.state !== "valid" && props.state !== "zero");
const notice = computed<{ type: "info" | "warning"; text: string } | null>(() => {
  switch (props.state) {
    case "inactive":
      return { type: "info", text: "实例已停用：下面是停用前最近一期的目标，不代表当前持仓，也不判断清仓结果。" };
    case "unknown":
      return { type: "warning", text: "无法确认当前会话的目标，请刷新；下面的数据仅供参考。" };
    case "empty":
      return { type: "info", text: "当前会话尚无策略结果，等待下一根 K 线触发计算。" };
    case "expired":
      return { type: "warning", text: "目标已经过期，仅作为历史参考；新一期结果产出后会自动更新。" };
    case "zero":
      return { type: "info", text: "当前会话明确输出零仓位目标。" };
    default:
      return null;
  }
});
</script>

<style scoped>
.target-section.is-stale .target-rows {
  opacity: 0.6;
}
.target-head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 8px 12px;
  margin-bottom: 10px;
}
.target-title {
  display: flex;
  align-items: center;
  gap: 8px;
}
.target-count,
.target-times {
  color: var(--color-text-3);
  font-size: 12px;
}
.target-times {
  display: flex;
  gap: 12px;
}
.target-alert {
  margin-bottom: 10px;
}
.target-rows {
  display: grid;
  border: 1px solid var(--color-border-2);
  border-radius: 6px;
}
.target-row {
  display: grid;
  grid-template-columns: minmax(120px, 1.2fr) minmax(80px, 2fr) 90px 48px;
  align-items: center;
  gap: 12px;
  padding: 7px 12px;
  font-size: 13px;
}
.target-row + .target-row {
  border-top: 1px solid var(--color-border-1);
}
.target-row--head {
  color: var(--color-text-3);
  font-size: 12px;
  background: var(--color-fill-1);
}
.target-id {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
}
.target-bar {
  height: 8px;
  overflow: hidden;
  border-radius: 4px;
  background: var(--color-fill-2);
}
.target-bar__fill {
  display: block;
  height: 100%;
  border-radius: 4px;
}
.target-bar__fill.long {
  background: rgb(var(--green-5));
}
.target-bar__fill.short {
  background: rgb(var(--red-5));
}
.target-weight-col {
  text-align: right;
}
.long {
  color: rgb(var(--green-6));
}
.short {
  color: rgb(var(--red-6));
}
@media (max-width: 640px) {
  .target-row {
    grid-template-columns: minmax(90px, 1fr) 70px 80px 32px;
  }
}
</style>
