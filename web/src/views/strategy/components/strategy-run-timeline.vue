<template>
  <a-table row-key="result_id" size="small" :data="results" :pagination="false" :scroll="{ x: 'max-content' }">
    <template #columns>
      <a-table-column title="结果 ID" data-index="result_id" :width="180" />
      <a-table-column title="触发时间" :width="180">
        <template #cell="{ record }">{{ formatTime(record.trigger_bar_time) }}</template>
      </a-table-column>
      <a-table-column title="动作" :width="100">
        <template #cell="{ record }"
          ><a-tag size="small">{{ record.action }}</a-tag></template
        >
      </a-table-column>
      <a-table-column title="命名空间" data-index="namespace" :width="140" />
      <a-table-column title="输入 Hash" data-index="input_hash" :width="180" :ellipsis="true" :tooltip="true" />
      <a-table-column title="命令序号" :width="110">
        <template #cell="{ record }">{{ record.command_sequence ?? "-" }}</template>
      </a-table-column>
      <a-table-column title="输出" :width="300" :ellipsis="true" :tooltip="true">
        <template #cell="{ record }">{{ record.output_json }}</template>
      </a-table-column>
    </template>
  </a-table>
  <a-empty v-if="!results.length" description="暂无策略结果" />
</template>

<script setup lang="ts">
import type { StrategyResult } from "@/api/strategy-types";
defineProps<{ results: StrategyResult[] }>();
function formatTime(value: string) {
  return value ? new Date(value).toLocaleString() : "-";
}
</script>
