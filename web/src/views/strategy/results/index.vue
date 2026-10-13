<template>
  <div class="moox-page strategy-results-page">
    <div class="moox-inner">
      <a-alert v-if="!spaceId" type="warning" show-icon>请先在顶部选择空间</a-alert>
      <template v-else>
        <a-alert v-if="listError" type="error" show-icon class="page-alert">
          {{ listError }}
          <template #action><a-button size="mini" @click="loadInstanceList">重试</a-button></template>
        </a-alert>

        <a-empty v-if="!tabs.length && !listLoading && !selectedId" description="当前空间还没有策略实例">
          <template #extra>
            <a-space>
              <a-button @click="router.push(instancesLink())">前往运行实例</a-button>
              <a-button type="primary" status="success" @click="router.push(instancesLink({ create: true }))">新建实例</a-button>
            </a-space>
          </template>
        </a-empty>

        <template v-else>
          <div class="result-toolbar">
            <span class="result-count">
              共 {{ tabs.length }} 个实例<span v-if="readAt">；状态读取：{{ readAt }}</span>
              <InfoTip text="每个实例的当前目标与逐期结果。已启用的实例排在前面；结果按 K 线逐期产出，页面每 10 秒自动刷新。" />
            </span>
            <a-space wrap>
              <a-switch v-model="autoRefresh" size="small" aria-label="自动刷新" />
              <span class="cell-sub" aria-hidden="true">自动刷新</span>
              <a-button :loading="listLoading || store.detailLoading" @click="refreshAll">
                <template #icon><icon-refresh /></template>
                刷新结果
              </a-button>
            </a-space>
          </div>

          <a-tabs :active-key="selectedId" type="rounded" size="medium" class="result-tabs" @change="selectInstance">
            <a-tab-pane v-for="tab in tabs" :key="tab.instanceId" :title="tab.title" />
          </a-tabs>

          <a-alert v-if="store.detailError" type="error" show-icon class="page-alert">
            实例读取失败：{{ store.detailError }}
            <template #action><a-button size="mini" @click="load">重试</a-button></template>
          </a-alert>

          <template v-if="current">
            <div class="instance-bar">
              <a-space wrap :size="12">
                <StatusBadge :enabled="current.enabled" :health="current.health" />
                <a-tag size="small" :color="current.logical_account_id ? 'orange' : 'blue'">{{
                  current.logical_account_id ? "交易模式" : "仅计算"
                }}</a-tag>
                <span class="cell-sub"
                  >定义 <span class="mono">{{ store.strategy?.name || current.strategy_id }}</span></span
                >
                <span class="cell-sub"
                  >View <span class="mono">{{ current.view_id }}</span></span
                >
                <span class="cell-sub"
                  >会话 <span class="mono">{{ current.session_id || "无当前会话" }}</span></span
                >
              </a-space>
              <a-space :size="16">
                <a-link @click="router.push(detailLink(current.instance_id))">实例详情与启停</a-link>
                <a-link @click="router.push(replayLink({ instanceId: current.instance_id, viewId: current.view_id }))"
                  >按会话回放</a-link
                >
              </a-space>
            </div>
            <InstanceResults
              :instance="current"
              :snapshot="store.targetSnapshot"
              :target-state="targetState"
              :results="store.results"
              :total="store.totalResults"
              :page="resultPage"
              :page-size="resultPageSize"
              :scope="resultScope"
              :loading="store.resultsLoading"
              :error="store.resultsError"
              @page="loadResults"
              @retry="loadResults(resultPage, resultScope)"
            />
          </template>
          <div v-else-if="store.detailLoading || listLoading" class="results-loading"><a-spin tip="正在读取结果" /></div>
        </template>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { StaleSpaceRequestError } from "@/api/admin/stale-space";
import { listInstances } from "@/api/strategy";
import type { StrategyInstance } from "@/api/strategy-types";
import { useSpaceStore } from "@/store/modules/space";
import { RequestGate } from "@/utils/request-gate";
import InfoTip from "@/views/factor/shared/info-tip.vue";
import InstanceResults from "@/views/strategy/components/strategy-instance-results.vue";
import StatusBadge from "@/views/strategy/components/strategy-status-badge.vue";
import { STRATEGY_TASKS_ROUTE, detailLink, instancesLink, normalizeTaskTab, replayLink } from "@/views/strategy/links";
import { queryString } from "@/views/strategy/query";
import { instanceTabs, resolveSelectedInstance } from "@/views/strategy/results/results-model";
import { useInstanceResults } from "@/views/strategy/use-instance-results";

defineOptions({ name: "StrategyResults" });

const PAGE_SIZE = 200;
const route = useRoute();
const router = useRouter();
const spaceStore = useSpaceStore();
const spaceId = computed(() => spaceStore.selectedSpaceId);

const instances = ref<StrategyInstance[]>([]);
const listLoading = ref(false);
const listLoaded = ref(false);
const listError = ref("");
const autoRefresh = ref(true);
const listGate = new RequestGate();

function isCurrentTab() {
  return route.name === STRATEGY_TASKS_ROUTE && normalizeTaskTab(route.query.tab) === "results";
}

// URL 的 instance 是选中实例的唯一来源；没有指定时，列表读到后写入默认实例。
const selectedId = computed(() => (isCurrentTab() ? queryString(route.query.instance as string) : ""));

const { store, resultPage, resultScope, resultPageSize, readAt, targetState, load, loadResults, start } = useInstanceResults(
  selectedId,
  { autoRefresh }
);

const tabs = computed(() => {
  const list = instanceTabs(instances.value, store.strategyCatalog);
  // URL 指定的实例不在列表里（例如已删除、仍可查看历史）：单独补一个 Tab，避免选中项无处显示。
  if (selectedId.value && !list.some(tab => tab.instanceId === selectedId.value)) {
    list.unshift({ instanceId: selectedId.value, title: `实例 ${selectedId.value.slice(0, 8)}`, enabled: false, health: "" });
  }
  return list;
});
const current = computed(() => (store.instance && store.instance.instance_id === selectedId.value ? store.instance : null));

function writeInstance(instanceId: string, mode: "push" | "replace" = "replace") {
  if (!isCurrentTab()) return;
  const query = { tab: "results", ...(instanceId ? { instance: instanceId } : {}) };
  void (mode === "push"
    ? router.push({ name: STRATEGY_TASKS_ROUTE, query })
    : router.replace({ name: STRATEGY_TASKS_ROUTE, query }));
}

function selectInstance(key: string | number) {
  writeInstance(String(key), "push");
}

/** 读取当前空间的全部实例（用作结果页的实例 Tab）。 */
async function loadInstanceList() {
  const token = listGate.next();
  listLoading.value = true;
  listError.value = "";
  try {
    const all: StrategyInstance[] = [];
    for (let page = 1; page <= 10; page += 1) {
      const rsp = await listInstances({ page, page_size: PAGE_SIZE });
      all.push(...rsp.items);
      if (!rsp.items.length || all.length >= rsp.page.total) break;
    }
    if (!listGate.isCurrent(token)) return;
    instances.value = all;
    listLoaded.value = true;
  } catch (error) {
    if (error instanceof StaleSpaceRequestError || !listGate.isCurrent(token)) return;
    listError.value = `实例列表读取失败：${error instanceof Error ? error.message : "未知错误"}`;
  } finally {
    if (listGate.isCurrent(token)) listLoading.value = false;
  }
}

async function loadCatalog() {
  if (store.strategiesComplete) return;
  await store.loadAllStrategies(200).catch(() => undefined);
}

function refreshAll() {
  void loadInstanceList();
  void load();
}

onMounted(() => {
  if (!spaceId.value) return;
  void loadInstanceList();
  void loadCatalog();
  if (selectedId.value) start();
});

// 列表读到、URL 没有指定实例时选中默认实例（切空间清掉 URL 后同样适用，不依赖两者的先后顺序）。
watch(
  () => [listLoaded.value, selectedId.value, tabs.value.length] as const,
  ([loaded, selected]) => {
    if (!loaded || selected || !isCurrentTab()) return;
    const fallback = resolveSelectedInstance("", tabs.value);
    if (fallback) writeInstance(fallback);
  }
);

// 空间初始化（无 → 有）只是首次加载，保留 URL 里的实例；真正切换空间时清掉旧空间的实例并重新选默认实例。
watch(spaceId, (next, previous) => {
  if (!next) return;
  listGate.next();
  instances.value = [];
  listLoaded.value = false;
  if (previous) writeInstance("");
  else if (selectedId.value) start();
  void loadInstanceList();
  void loadCatalog();
});
</script>

<style scoped lang="scss">
@use "../shared/strategy-page.scss";

.result-tabs {
  margin-bottom: var(--moox-space-3);
}

.result-tabs :deep(.arco-tabs-content) {
  display: none;
}

.instance-bar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: var(--moox-space-2);
  margin-bottom: var(--moox-space-3);
  padding: var(--moox-space-2) var(--moox-space-3);
  border-radius: 6px;
  background: var(--color-fill-1);
}

.results-loading {
  display: flex;
  justify-content: center;
  padding: 48px 0;
}
</style>
