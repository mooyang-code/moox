<template>
  <div class="instance-results">
    <a-alert v-if="!instance.enabled && instance.session_id" type="warning" show-icon class="results-alert"
      >实例已经停用，但会话仍存在。后台账户释放可能尚未完成，请不要将旧目标当作当前有效仓位。</a-alert
    >
    <a-alert v-if="instance.health === 'degraded'" type="error" show-icon class="results-alert"
      >最近一期被跳过。请在下方历史结果中查看具体跳过原因：数据暂时不足等情况会在后续周期自动恢复，仅当原因是因子定义版本变化时才需要停用并重新启用实例。</a-alert
    >
    <div class="results-card">
      <TargetTable :targets="snapshot?.targets || []" :snapshot="snapshot" :state="targetState" :loading="initialLoading" />
    </div>
    <div class="results-card">
      <div class="results-card__title">
        <strong>历史结果</strong><span class="muted">每根 K 线一期，含跳过原因与逐标的解释</span>
      </div>
      <ResultTimeline
        :results="results"
        :total="total"
        :page="page"
        :page-size="pageSize"
        :scope="scope"
        :loading="loading || initialLoading"
        :error="error"
        @page="(next, nextScope) => emit('page', next, nextScope)"
        @retry="emit('retry')"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import type { StrategyInstance, StrategyResult, StrategyTargetSnapshot } from "@/api/strategy-types";
import ResultTimeline from "@/views/strategy/components/strategy-result-table.vue";
import TargetTable from "@/views/strategy/components/strategy-target-table.vue";
import type { TargetState } from "@/views/strategy/model";

// 实例的结果面板：上面是当前目标，下面直接是历史结果，不再藏在标签页里。数据由页面（useInstanceResults）提供。
defineProps<{
  instance: StrategyInstance;
  snapshot: StrategyTargetSnapshot | null;
  targetState: TargetState;
  results: StrategyResult[];
  total: number;
  page: number;
  pageSize: number;
  scope: "session" | "all";
  loading: boolean;
  error: string;
  /** 首次读取尚未完成：目标与结果都显示读取中。 */
  initialLoading?: boolean;
}>();
const emit = defineEmits<{ page: [number, "session" | "all"]; retry: [] }>();
</script>

<style scoped>
.instance-results {
  display: grid;
  gap: var(--moox-space-3);
}
.results-alert {
  margin-bottom: 0;
}
.results-card {
  min-width: 0;
  padding: var(--moox-space-3);
  border: 1px solid var(--color-border-2);
  border-radius: 6px;
}
.results-card__title {
  display: flex;
  align-items: baseline;
  gap: 10px;
  margin-bottom: 10px;
}
.muted {
  color: var(--color-text-3);
  font-size: 12px;
}
</style>
