<template>
  <div class="target-head">
    <span>FULL 快照，共 {{ targets.length }} 个 Instrument</span>
    <span class="target-meta" v-if="hasResult">
      <a-tag v-if="barEndTime" size="small">bar {{ formatTime(barEndTime) }}</a-tag>
      <a-tag v-if="validUntil" size="small">有效至 {{ formatTime(validUntil) }}</a-tag>
      <a-tag v-else-if="commandSequence" size="small">sequence {{ commandSequence }}</a-tag>
    </span>
  </div>
  <a-alert v-if="!hasResult" type="info" show-icon> 暂无成功策略结果。 </a-alert>
  <a-alert v-else-if="targets.length === 0" type="info" show-icon> 空 FULL 目标：所有既有 Instrument 的期望持仓均为零。 </a-alert>
  <a-table v-else-if="hasResult" size="small" :data="targets" :pagination="false" row-key="instrument_id">
    <template #columns>
      <a-table-column title="Instrument" data-index="instrument_id" />
      <a-table-column title="目标权重" data-index="target_weight" />
    </template>
  </a-table>
</template>

<script setup lang="ts">
import type { InstrumentTarget } from "@/api/strategy-types";
withDefaults(defineProps<{ targets: InstrumentTarget[]; commandSequence?: string; barEndTime?: string; validUntil?: string; hasResult?: boolean }>(), { hasResult: true });
function formatTime(value?: string) {
  return value ? new Date(value).toLocaleString() : "-";
}
</script>

<style scoped>
.target-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 10px;
  color: var(--color-text-2);
}
.target-meta {
  display: inline-flex;
  gap: 6px;
}
</style>
