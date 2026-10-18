<template>
  <div class="moox-page factor-results-page">
    <div class="moox-inner">
      <a-alert v-if="!spaceId" type="warning" show-icon>请先在顶部选择空间</a-alert>
      <template v-else>
        <a-alert v-if="store.loadError" type="error" show-icon class="results-alert">
          {{ store.loadError }}
          <template #action><a-button size="mini" @click="reload">重试</a-button></template>
        </a-alert>

        <a-empty v-if="!tabs.length && !store.loading" description="暂无计算任务">
          <template #extra>
            <a-button @click="goTasks">前往计算任务</a-button>
          </template>
        </a-empty>

        <template v-else-if="tabs.length">
          <div class="result-toolbar">
            <span class="result-count"
              >共 {{ tabs.length }} 个计算任务<span v-if="readAt">；状态读取：{{ readAt }}</span></span
            >
            <a-button :loading="viewLoading" @click="refreshResults">
              <template #icon><icon-refresh /></template>
              刷新结果
            </a-button>
          </div>
          <a-tabs v-model:active-key="activeSetId" type="rounded" size="medium" class="result-tabs" @change="onTabChange">
            <a-tab-pane v-for="tab in tabs" :key="tab.setId" :title="tab.title" />
          </a-tabs>

          <ViewBrowse
            v-if="resultView"
            :key="`${resultView.space_id}/${resultView.view_id}`"
            :embedded="true"
            :view-ids="[resultView.view_id]"
            :active-view-id="resultView.view_id"
            :refresh-key="refreshKey"
            :view-owner-modules="['factor']"
            :view-roles="['factor_result']"
            :auto-refresh-interval-ms="60000"
            empty-description="结果视图准备中"
            empty-rows-description="计算任务已准备，尚未产生数据"
          >
            <template #status-extra>
              <a-tag v-if="summary.hasPeriod" size="small">最近周期 {{ summary.lastPeriod }}</a-tag>
              <template v-if="summary.total">
                <a-tag size="small" color="green">正常 {{ summary.complete }}</a-tag>
                <a-tag v-if="summary.degraded" size="small" color="orange">降级 {{ summary.degraded }}</a-tag>
                <a-tag v-if="summary.skipped" size="small" color="gray">跳过 {{ summary.skipped }}</a-tag>
              </template>
            </template>
          </ViewBrowse>
          <a-spin v-else-if="viewLoading" class="results-loading" />
          <a-empty v-else description="结果视图由 Storage 在结果数据集激活后自动创建，请稍后刷新">
            <template #extra>
              <a-button @click="goTasks">前往计算任务</a-button>
            </template>
          </a-empty>
        </template>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useRouter } from "vue-router";
import { Message } from "@arco-design/web-vue";
import type { FactorSetInfo } from "@/api/factor/types";
import { listViews } from "@/api/storage/metadata";
import type { View } from "@/api/storage/types";
import { RequestGate } from "@/utils/request-gate";
import ViewBrowse from "@/views/data/view-browse/index.vue";
import { useFactorScope } from "@/views/factor/shared/use-factor-scope";
import { pickResultView, resolveActiveSetId, resultSets, resultSummary, resultTabTitle } from "./results-model";

defineOptions({ name: "FactorResults" });

const router = useRouter();
const { store, spaceId, selectSet, reload } = useFactorScope({ requireSet: true });

const available = computed<FactorSetInfo[]>(() => resultSets(store.sets));
const tabs = computed(() =>
  available.value.map((info: FactorSetInfo) => ({
    setId: info.factor_set.set_id,
    title: resultTabTitle(info, store.setLabel(info.factor_set))
  }))
);
const activeSetId = ref("");
const activeInfo = computed(() => available.value.find((item: FactorSetInfo) => item.factor_set.set_id === activeSetId.value));
const summary = computed(() => resultSummary(activeInfo.value ?? {}));

const resultView = ref<View | null>(null);
const viewLoading = ref(false);
const refreshKey = ref(0);
const readAt = ref("");
const viewGate = new RequestGate();

function stamp() {
  readAt.value = new Date().toLocaleTimeString("zh-CN", { hour12: false });
}

async function loadResultView() {
  const current = activeInfo.value?.factor_set;
  const token = viewGate.next();
  resultView.value = null;
  if (!current) {
    viewLoading.value = false;
    return;
  }
  viewLoading.value = true;
  try {
    const rsp = await listViews({
      space_id: current.space_id,
      dataset_id: current.result_dataset_id,
      status: "active",
      page: { page: 1, size: 100 }
    });
    if (!viewGate.isCurrent(token)) return;
    resultView.value = pickResultView(rsp.views || [], current);
  } catch (error) {
    if (viewGate.isCurrent(token)) Message.error(error instanceof Error ? error.message : "结果视图加载失败");
  } finally {
    if (viewGate.isCurrent(token)) viewLoading.value = false;
  }
}

function onTabChange(key: string | number) {
  selectSet(String(key));
}

async function refreshResults() {
  await store.reload();
  await loadResultView();
  refreshKey.value += 1;
}

function goTasks() {
  void router.push("/factor/tasks");
}

// 选中项跟随 store.currentSetId（?set= 已由 use-factor-scope 同步）；若它不在可浏览集合里，回落到第一个。
watch(
  () => [store.currentSetId, available.value.map((item: FactorSetInfo) => item.factor_set.set_id).join(",")],
  () => {
    const next = resolveActiveSetId(available.value, store.currentSetId);
    if (next !== activeSetId.value) activeSetId.value = next;
  },
  { immediate: true }
);

watch(
  () => activeInfo.value?.factor_set.result_dataset_id,
  () => void loadResultView(),
  { immediate: true }
);

watch(() => store.sets, stamp, { immediate: true });
</script>

<style scoped lang="scss">
@use "../shared/factor-page.scss";

.factor-results-page {
  width: 100%;
  max-width: 100%;
  min-width: 0;
  min-height: 100%;
  overflow-x: hidden;
}

.results-alert {
  margin-bottom: var(--moox-space-3);
}

.result-tabs {
  min-width: 0;
  margin-bottom: var(--moox-space-3);
}

.result-tabs :deep(.arco-tabs-content) {
  display: none;
}

.results-loading {
  display: flex;
  justify-content: center;
  padding: var(--moox-space-8);
}
</style>
