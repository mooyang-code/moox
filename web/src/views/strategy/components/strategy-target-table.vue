<template>
  <a-card title="目标持仓" :bordered="false">
    <a-table size="small" :data="targets" :pagination="false" row-key="instrument_id">
      <template #columns>
        <a-table-column title="Instrument" data-index="instrument_id" />
        <a-table-column title="Symbol" data-index="symbol" />
        <a-table-column title="目标数量" data-index="target_quantity" />
        <a-table-column title="原因" data-index="reason" />
        <a-table-column title="数据版本" data-index="data_revision" />
      </template>
    </a-table>
    <a-empty v-if="!targets.length" description="暂无目标数据" />
  </a-card>
</template>

<script setup lang="ts">
import type { TargetPosition } from "@/api/strategy-types";
defineProps<{ targets: TargetPosition[] }>();
</script>
