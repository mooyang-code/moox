<template>
  <div class="strategy-page">
    <div class="moox-inner">
      <div class="page-head">
        <div>
          <h2>{{ overview?.definition?.strategy_id || "策略详情" }}</h2>
          <span>{{ overview?.definition?.version || "" }} · {{ overview?.binding?.binding_id || bindingId }}</span>
        </div>
        <a-space
          ><a-button @click="goBack">返回</a-button><a-button :loading="store.loading" @click="refresh">刷新</a-button></a-space
        >
      </div>
      <a-alert v-if="store.error" type="error" show-icon class="top-alert">{{ store.error }}</a-alert>
      <a-grid :cols="2" :col-gap="12" :row-gap="12">
        <a-grid-item
          ><a-card title="策略信息" :bordered="false"
            ><a-descriptions :column="2" size="small"
              ><a-descriptions-item label="策略 ID">{{ overview?.definition?.strategy_id || "-" }}</a-descriptions-item
              ><a-descriptions-item label="版本">{{ overview?.definition?.version || "-" }}</a-descriptions-item
              ><a-descriptions-item label="源码 Hash">{{ overview?.definition?.source_hash || "-" }}</a-descriptions-item
              ><a-descriptions-item label="Binding">{{ overview?.binding?.binding_id || "-" }}</a-descriptions-item
              ><a-descriptions-item label="Space">{{ overview?.binding?.space_id || "-" }}</a-descriptions-item
              ><a-descriptions-item label="频率">{{ overview?.binding?.freq || "-" }}</a-descriptions-item></a-descriptions
            ></a-card
          ></a-grid-item
        >
        <a-grid-item><HealthPanel :health="overview?.health" /></a-grid-item>
      </a-grid>
      <div class="operation-panel">
        <OperationPanel
          :binding-id="bindingId"
          :status="overview?.binding?.status"
          :current-mode="overview?.health?.mode"
          @changed="refresh"
        />
      </div>
      <a-tabs class="detail-tabs" default-active-key="runs">
        <a-tab-pane key="runs" title="决策记录"><RunTimeline :runs="store.runs" /></a-tab-pane>
        <a-tab-pane key="targets" title="目标与持仓"><TargetTable :targets="targets" /></a-tab-pane>
        <a-tab-pane key="state" title="状态与数据"><StateSummary :state="overview?.state" /></a-tab-pane>
        <a-tab-pane key="performance" title="策略表现"><PerformancePanel :binding-id="bindingId" /></a-tab-pane>
      </a-tabs>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, onUnmounted, ref } from "vue";
import { useRoute, useRouter } from "vue-router";
import { storeToRefs } from "pinia";
import { listStrategyTargets } from "@/api/strategy";
import { useStrategyStore } from "@/store/modules/strategy";
import type { TargetPosition } from "@/api/strategy-types";
import HealthPanel from "@/views/strategy/components/strategy-health-panel.vue";
import RunTimeline from "@/views/strategy/components/strategy-run-timeline.vue";
import TargetTable from "@/views/strategy/components/strategy-target-table.vue";
import StateSummary from "@/views/strategy/components/strategy-state-summary.vue";
import PerformancePanel from "@/views/strategy/performance/index.vue";
import OperationPanel from "@/views/strategy/components/strategy-operation-panel.vue";

defineOptions({ name: "StrategyDetail" });
const route = useRoute();
const router = useRouter();
const store = useStrategyStore();
const bindingId = String(route.params.bindingId || "");
const { overview } = storeToRefs(store);
const targets = ref<TargetPosition[]>([]);

async function refresh() {
  await store.loadOverview(bindingId);
  const latestRun = store.runs[0];
  if (latestRun) targets.value = (await listStrategyTargets(latestRun.run_id, { page: 1, page_size: 100 })).items;
}
function goBack() {
  router.push({ name: "strategy-running" });
}
onMounted(() => {
  refresh();
  store.startPolling(refresh, 5000);
});
onUnmounted(() => store.stopPolling());
</script>

<style scoped>
.strategy-page {
  min-height: 100%;
  background: var(--color-fill-2);
}
.detail-tabs {
  margin-top: var(--moox-space-3);
}
.operation-panel {
  margin-top: var(--moox-space-3);
}
.top-alert {
  margin-bottom: var(--moox-space-3);
}
</style>
