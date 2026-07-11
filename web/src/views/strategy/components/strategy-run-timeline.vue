<template>
  <a-card title="最近决策" :bordered="false">
    <a-table size="small" :data="runs" :pagination="false" row-key="run_id">
      <template #columns>
        <a-table-column title="触发时间" data-index="trigger_bar_time" :width="190" />
        <a-table-column title="运行 ID" data-index="run_id" :width="180" :ellipsis="true" :tooltip="true" />
        <a-table-column title="决策" data-index="action" :width="100" />
        <a-table-column title="状态" data-index="status" :width="120" />
        <a-table-column title="数据版本" data-index="data_revision" :width="180" :ellipsis="true" :tooltip="true" />
      </template>
    </a-table>
    <a-empty v-if="!runs.length" description="暂无运行记录" />
  </a-card>
</template>

<script setup lang="ts">
import type { StrategyRun } from '@/api/strategy-types';
defineProps<{ runs: StrategyRun[] }>();
</script>
