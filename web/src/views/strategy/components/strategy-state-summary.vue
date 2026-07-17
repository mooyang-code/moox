<template>
  <a-card title="状态与数据" :bordered="false">
    <a-descriptions :column="2" size="small">
      <a-descriptions-item label="State revision">{{ state?.revision ?? "-" }}</a-descriptions-item>
      <a-descriptions-item label="最近运行">{{ state?.last_run_id || "-" }}</a-descriptions-item>
      <a-descriptions-item label="状态大小">{{
        state?.state_json ? `${state.state_json.length} bytes` : "0 bytes"
      }}</a-descriptions-item>
      <a-descriptions-item label="状态内容">仅提供摘要，禁止在线编辑</a-descriptions-item>
    </a-descriptions>
  </a-card>
</template>

<script setup lang="ts">
import type { StrategyState } from "@/api/strategy-types";
defineProps<{ state?: StrategyState }>();
</script>
