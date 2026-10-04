<template>
  <div v-if="info" class="results-tab">
    <div class="status-strip">
      <span>结果数据集 {{ set.result_dataset_id }}</span>
      <span>结果 View {{ resultView?.view_id || "准备中" }}</span>
      <span>最近周期 {{ formatPeriod(info.last_run?.last_period_time) }}</span>
      <span v-if="summary.total">
        本周期因子：
        <a-tag size="small" color="green">正常 {{ summary.complete }}</a-tag>
        <a-tag v-if="summary.degraded" size="small" color="orange">降级 {{ summary.degraded }}</a-tag>
        <a-tag v-if="summary.skipped" size="small" color="gray">跳过 {{ summary.skipped }}</a-tag>
      </span>
      <a-button size="mini" aria-label="刷新结果 View" @click="loadResultView">
        <template #icon><icon-refresh /></template>
      </a-button>
    </div>
    <ViewBrowse
      v-if="resultView"
      :key="`${set.space_id}/${resultView.view_id}`"
      :embedded="true"
      page-title="因子结果数据"
      empty-description="Storage 尚未提供该因子集的默认结果 View"
      :view-ids="[resultView.view_id]"
      :view-owner-modules="['factor']"
      :view-roles="['factor_result']"
      :auto-refresh-interval-ms="60000"
    />
    <a-spin v-else-if="loading" class="results-tab__loading" />
    <a-empty v-else description="结果 View 由 Storage 在结果数据集激活后自动创建；因子集刚创建或处于创建中时请稍候刷新" />
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import type { FactorSet, FactorSetInfo } from "@/api/factor/types";
import { listViews } from "@/api/storage/metadata";
import type { View } from "@/api/storage/types";
import { useFactorStore } from "@/store/modules/factor";
import { RequestGate } from "@/utils/request-gate";
import ViewBrowse from "@/views/data/view-browse/index.vue";
import { formatPeriod } from "../health";

defineOptions({ name: "FactorResultsTab" });

const store = useFactorStore();
const info = computed<FactorSetInfo | undefined>(() => store.current);
const set = computed<FactorSet>(() => info.value!.factor_set);
const resultView = ref<View | null>(null);
const loading = ref(false);
const gate = new RequestGate();

const summary = computed(() => {
  const states = info.value?.last_run?.factors || [];
  return {
    total: states.length,
    complete: states.filter(state => state.status === "complete").length,
    degraded: states.filter(state => state.status === "degraded").length,
    skipped: states.filter(state => state.status === "skipped").length
  };
});

async function loadResultView() {
  const current = info.value?.factor_set;
  const token = gate.next();
  resultView.value = null;
  if (!current) {
    loading.value = false;
    return;
  }
  loading.value = true;
  try {
    const rsp = await listViews({
      space_id: current.space_id,
      dataset_id: current.result_dataset_id,
      status: "active",
      page: { page: 1, size: 100 }
    });
    if (!gate.isCurrent(token)) return;
    resultView.value =
      (rsp.views || []).find(
        view => view.attributes?.owner_module === "factor" && view.attributes?.view_role === "factor_result"
      ) || null;
  } catch (error) {
    if (gate.isCurrent(token)) Message.error(error instanceof Error ? error.message : "结果 View 加载失败");
  } finally {
    if (gate.isCurrent(token)) loading.value = false;
  }
}

watch(() => info.value?.factor_set.set_id, loadResultView, { immediate: true });
</script>

<style scoped>
.results-tab {
  display: flex;
  min-height: 0;
  flex-direction: column;
}

.status-strip {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--moox-space-4);
  margin-bottom: var(--moox-space-3);
  color: var(--color-text-2);
  font-size: 13px;
}

.results-tab__loading {
  display: flex;
  justify-content: center;
  padding: var(--moox-space-8);
}
</style>
