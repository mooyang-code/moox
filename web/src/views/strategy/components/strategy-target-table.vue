<template>
  <a-card title="目标与持仓" :bordered="false">
    <a-alert v-if="targets.length && !hasComparison" type="info" show-icon class="source-note">理论目标来自最近一次 Python 决策；当前暂无 Trade 组合快照。</a-alert>
    <a-table size="small" :data="targets" :pagination="false" row-key="instrument_id">
      <template #columns>
        <a-table-column title="Instrument" data-index="instrument_id" />
        <a-table-column title="策略理论目标" data-index="target_weight" />
        <a-table-column title="组合目标"><template #cell="{ record }">{{ record.portfolio_target || '暂无快照' }}</template></a-table-column>
        <a-table-column title="实际持仓"><template #cell="{ record }">{{ record.actual_position || '暂无快照' }}</template></a-table-column>
        <a-table-column title="偏差"><template #cell="{ record }">{{ record.deviation || '暂无快照' }}</template></a-table-column>
      </template>
    </a-table>
    <a-empty v-if="!targets.length" description="暂无目标数据" />
  </a-card>
</template>

<script setup lang="ts">
import { computed } from 'vue';
import type { TargetWeight } from '@/api/strategy-types';
const props = defineProps<{ targets: TargetWeight[] }>();
const hasComparison = computed(() => props.targets.some((target) => target.portfolio_target || target.actual_position || target.deviation));
</script>

<style scoped>
.source-note { margin-bottom: 12px; }
</style>
