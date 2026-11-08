<template>
  <div class="instance-results">
    <a-alert v-if="!instance.enabled && instance.session_id" type="warning" show-icon class="results-alert"
      >实例已经停用，但会话仍存在。后台账户释放可能尚未完成，请不要将旧目标当作当前有效仓位。</a-alert
    >
    <a-alert v-if="instance.health === 'degraded'" type="error" show-icon class="results-alert">{{
      skipAdvice(latestSkip?.skip_reason)
    }}</a-alert>
    <a-alert v-else-if="instance.enabled && targetState === 'expired'" type="warning" show-icon class="results-alert">
      目标已过期：最近一期之后没有产出新结果，多半是上游因子任务没有产出。查看因子任务：
      <a-link v-for="taskId in upstreamTasks" :key="taskId" :href="`#/factor/tasks?detail=${encodeURIComponent(taskId)}`">{{
        taskId
      }}</a-link>
    </a-alert>
    <div class="results-card">
      <TargetTable :targets="snapshot?.targets || []" :snapshot="snapshot" :state="targetState" :loading="initialLoading" />
    </div>
    <PoolDetail v-if="poolPeriods.length" :periods="poolPeriods" />
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
import { computed } from "vue";
import type { StrategyInstance, StrategyResult, StrategyTargetSnapshot } from "@/api/strategy-types";
import PoolDetail from "@/views/strategy/components/strategy-pool-detail.vue";
import ResultTimeline from "@/views/strategy/components/strategy-result-table.vue";
import TargetTable from "@/views/strategy/components/strategy-target-table.vue";
import { skipAdvice, type TargetState } from "@/views/strategy/model";
import { buildPoolPeriods } from "@/views/strategy/pool-model";

// 实例的结果面板：上面是当前目标，策略池的实例中间多一块策略池详情，下面直接是历史结果，不再藏在标签页里。数据由页面（useInstanceResults）提供。
const props = defineProps<{
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
// 策略池的实例：已加载的结果里带 pool 明细的期，没有时整块不显示。
const poolPeriods = computed(() => buildPoolPeriods(props.results));
// 本会话最新一期被跳过时的那条结果：用它的跳过原因写说明和下一步
// 只有在看本会话的第一页时，首条才是最近一期；翻到别的页或看全部历史时写通用说明
const latestSkip = computed(() => {
  if (props.page !== 1 || props.scope !== "session") return undefined;
  const latest = props.results.find(item => item.session_id === props.instance.session_id);
  return latest?.status === "skipped" ? latest : undefined;
});
const upstreamTasks = computed(() => [...new Set(Object.values(props.instance.factor_tasks || {}))]);
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
