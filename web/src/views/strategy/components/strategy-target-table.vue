<template>
  <a-card title="目标与持仓" :bordered="false">
    <a-alert v-if="targets.length" type="info" show-icon class="source-note">理论目标来自最近一次 Python 决策；组合目标和实际持仓将在 Trade 快照接入后展示。</a-alert>
    <a-table size="small" :data="targets" :pagination="false" row-key="instrument_id">
      <template #columns>
        <a-table-column title="Instrument" data-index="instrument_id" />
        <a-table-column title="策略理论目标" data-index="target_weight" />
        <a-table-column title="组合目标" :render="() => '暂无快照'" />
        <a-table-column title="实际持仓" :render="() => '暂无快照'" />
        <a-table-column title="偏差" :render="() => '暂无快照'" />
      </template>
    </a-table>
    <a-empty v-if="!targets.length" description="暂无目标数据" />
  </a-card>
</template>

<script setup lang="ts">
import type { TargetWeight } from '@/api/strategy-types';
defineProps<{ targets: TargetWeight[] }>();
</script>

<style scoped>
.source-note { margin-bottom: 12px; }
</style>
