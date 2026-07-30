<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="page-head">
        <div>
          <a-button type="text" @click="router.push({ name: 'strategy-running' })">
            <template #icon><icon-left /></template>返回
          </a-button>
          <h2>{{ store.runner?.runner_id || runnerId }}</h2>
          <span>{{ store.strategy?.name || store.runner?.strategy_id || "-" }}</span>
        </div>
        <a-button :loading="store.loading" @click="load"
          ><template #icon><icon-refresh /></template>刷新</a-button
        >
      </div>
      <a-alert v-if="store.error" type="error" show-icon class="top-alert">{{ store.error }}</a-alert>
      <a-descriptions v-if="store.runner" :column="3" bordered class="summary">
        <a-descriptions-item label="状态"><StatusBadge :status="store.runner.status" /></a-descriptions-item>
        <a-descriptions-item label="数据视图">{{ store.runner.view_id }}</a-descriptions-item>
        <a-descriptions-item label="频率">{{ store.runner.frequency }}</a-descriptions-item>
        <a-descriptions-item label="Logical Account">{{ store.runner.logical_account_id || "观察模式" }}</a-descriptions-item>
        <a-descriptions-item label="命令序号">{{ store.commandSequence }}</a-descriptions-item>
        <a-descriptions-item label="最近成功">{{ formatTime(store.runner.last_success_at) }}</a-descriptions-item>
      </a-descriptions>
      <a-alert v-if="store.runner?.last_error" type="error" show-icon class="top-alert">{{ store.runner.last_error }}</a-alert>
      <OperationPanel v-if="store.runner" :runner-id="runnerId" :status="store.runner.status" @changed="load" />
      <a-tabs default-active-key="targets">
        <a-tab-pane key="targets" title="当前完整目标">
          <TargetTable :targets="store.targets" :command-sequence="store.commandSequence" />
        </a-tab-pane>
        <a-tab-pane key="results" title="策略结果">
          <ResultTimeline :results="store.results" />
        </a-tab-pane>
        <a-tab-pane key="params" title="参数">
          <pre>{{ prettyJSON(store.runner?.params_json) }}</pre>
        </a-tab-pane>
      </a-tabs>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, onUnmounted } from "vue";
import { useRoute, useRouter } from "vue-router";
import { useStrategyStore } from "@/store/modules/strategy";
import OperationPanel from "@/views/strategy/components/strategy-operation-panel.vue";
import ResultTimeline from "@/views/strategy/components/strategy-run-timeline.vue";
import StatusBadge from "@/views/strategy/components/strategy-status-badge.vue";
import TargetTable from "@/views/strategy/components/strategy-target-table.vue";

defineOptions({ name: "StrategyDetail" });
const route = useRoute();
const router = useRouter();
const store = useStrategyStore();
const runnerId = String(route.params.runnerId || "");
const load = () => store.loadRunnerDetail(runnerId);

function formatTime(value?: string) {
  return value ? new Date(value).toLocaleString() : "-";
}
function prettyJSON(value?: string) {
  if (!value) return "{}";
  try {
    return JSON.stringify(JSON.parse(value), null, 2);
  } catch {
    return value;
  }
}
onMounted(() => {
  load();
  store.startPolling(load);
});
onUnmounted(() => store.stopPolling());
</script>

<style scoped>
.page-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: var(--moox-space-2);
}
.page-head h2 {
  margin: 8px 0 4px;
}
.page-head span {
  color: var(--color-text-3);
}
.summary,
.top-alert {
  margin-bottom: var(--moox-space-2);
}
pre {
  overflow: auto;
  padding: 12px;
  background: var(--color-fill-2);
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
}
</style>
