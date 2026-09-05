<template>
  <a-table row-key="result_id" size="small" :data="results" :pagination="false" :scroll="{ x: 'max-content' }">
    <template #columns>
      <a-table-column title="结果 ID" data-index="result_id" :width="180" />
      <a-table-column title="决策周期" :width="180">
        <template #cell="{ record }">{{ formatTime(record.bar_end_time || record.period_time) }}</template>
      </a-table-column>
      <a-table-column title="投递状态" :width="150">
        <template #cell="{ record }"
          ><a-tag size="small">{{ deliveryStatus(record.publish_status, record.action) }}</a-tag></template
        >
      </a-table-column>
      <a-table-column title="运行会话" data-index="session_id" :width="180" :ellipsis="true" :tooltip="true" />
      <a-table-column title="有效至" :width="180">
        <template #cell="{ record }">{{ formatTime(record.valid_until) }}</template>
      </a-table-column>
      <a-table-column title="目标数" :width="90">
        <template #cell="{ record }">{{ record.targets.length }}</template>
      </a-table-column>
      <a-table-column title="诊断" :width="300" :ellipsis="true" :tooltip="true">
        <template #cell="{ record }">{{ record.debug_info_json || record.rule_states_json || "-" }}</template>
      </a-table-column>
    </template>
  </a-table>
  <a-empty v-if="!results.length" description="暂无策略结果" />
</template>

<script setup lang="ts">
import type { StrategyResult } from "@/api/strategy-types";
defineProps<{ results: StrategyResult[] }>();
function deliveryStatus(status?: string, action?: string) {
  const labels: Record<string, string> = {
    sent: "Broker 已确认",
    pending: "待投递",
    cancelled: "已取消投递",
    none: "无需投递"
  };
  return status ? labels[status] || status : action || "-";
}
function formatTime(value?: string) {
  return value ? new Date(value).toLocaleString() : "-";
}
</script>
