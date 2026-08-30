<template>
  <div class="target-head">
    <span>FULL 快照，共 {{ targets.length }} 个 Instrument</span>
    <a-tag size="small">sequence {{ commandSequence }}</a-tag>
  </div>
  <a-alert v-if="targets.length === 0" type="info" show-icon> 空 FULL 目标：所有既有 Instrument 的期望持仓均为零。 </a-alert>
  <a-table v-else size="small" :data="targets" :pagination="false" row-key="instrument_id">
    <template #columns>
      <a-table-column title="Instrument" data-index="instrument_id" />
      <a-table-column title="目标权重" data-index="target_weight" />
    </template>
  </a-table>
</template>

<script setup lang="ts">
import type { InstrumentTarget } from "@/api/strategy-types";
defineProps<{ targets: InstrumentTarget[]; commandSequence: string }>();
</script>

<style scoped>
.target-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 10px;
  color: var(--color-text-2);
}
</style>
