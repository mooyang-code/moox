<template>
  <div class="target-head">
    <div><strong>最新目标权重</strong><span class="target-count">{{ targets.length }} 个标的</span></div>
    <a-space v-if="snapshot">
      <a-tag v-if="snapshot.bar_end_time" size="small">K 线结束 {{ formatStrategyTime(snapshot.bar_end_time) }}</a-tag>
      <a-tag v-if="snapshot.valid_until" size="small">有效至 {{ formatStrategyTime(snapshot.valid_until) }}</a-tag>
    </a-space>
  </div>
  <a-alert v-if="state === 'inactive'" type="info" show-icon>实例已停用。这里不判断清仓结果。</a-alert>
  <a-alert v-else-if="state === 'unknown'" type="warning" show-icon>无法确认当前会话目标，请刷新实例和目标数据。</a-alert>
  <a-alert v-else-if="state === 'empty'" type="info" show-icon>当前会话尚无策略结果。</a-alert>
  <a-alert v-else-if="state === 'expired'" type="warning" show-icon>目标已经过期，仅作为历史参考。</a-alert>
  <a-alert v-else-if="state === 'zero'" type="info" show-icon>当前会话明确输出零仓位目标。</a-alert>
  <a-table v-if="state === 'valid'" size="small" :data="targets" :pagination="false" row-key="instrument_id" :scroll="{ x: 520 }">
    <template #columns>
      <a-table-column title="标的" data-index="instrument_id" :width="260" />
      <a-table-column title="目标权重" :width="180"><template #cell="{ record }"><span :class="Number(record.target_weight) < 0 ? 'short' : 'long'">{{ targetWeightPercent(record) }}</span></template></a-table-column>
      <a-table-column title="方向" :width="100"><template #cell="{ record }">{{ Number(record.target_weight) < 0 ? "空" : "多" }}</template></a-table-column>
    </template>
  </a-table>
</template>

<script setup lang="ts">
import type { InstrumentTarget, StrategyTargetSnapshot } from "@/api/strategy-types";
import { formatStrategyTime, targetWeightPercent, type TargetState } from "@/views/strategy/model";
defineProps<{ targets: InstrumentTarget[]; snapshot: StrategyTargetSnapshot | null; state: TargetState }>();
</script>

<style scoped>
.target-head { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 12px; }
.target-head > div { display: flex; align-items: baseline; gap: 10px; }
.target-count { color: var(--color-text-3); font-size: 12px; }
.long { color: rgb(var(--green-6)); }
.short { color: rgb(var(--red-6)); }
@media (max-width: 640px) { .target-head { align-items: flex-start; flex-direction: column; } }
</style>
