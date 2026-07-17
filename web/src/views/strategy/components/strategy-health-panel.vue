<template>
  <a-card title="运行健康" :bordered="false">
    <a-descriptions :column="2" size="small">
      <a-descriptions-item label="状态"><StatusBadge :status="health?.status" /></a-descriptions-item>
      <a-descriptions-item label="模式">{{ health?.mode || "-" }}</a-descriptions-item>
      <a-descriptions-item label="Worker">{{ health?.worker_status || "-" }}</a-descriptions-item>
      <a-descriptions-item label="Outbox 延迟">{{ health?.outbox_lag_seconds ?? 0 }}s</a-descriptions-item>
      <a-descriptions-item label="数据版本">{{ health?.last_data_revision || "-" }}</a-descriptions-item>
      <a-descriptions-item label="数据截止">{{ health?.data_cutoff || "-" }}</a-descriptions-item>
      <a-descriptions-item v-if="health?.last_error_message" :span="2" label="最近错误"
        >{{ health.last_error_type }}: {{ health.last_error_message }}</a-descriptions-item
      >
    </a-descriptions>
  </a-card>
</template>

<script setup lang="ts">
import type { StrategyHealth } from "@/api/strategy-types";
import StatusBadge from "./strategy-status-badge.vue";
defineProps<{ health?: StrategyHealth }>();
</script>
