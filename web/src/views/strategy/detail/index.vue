<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="page-head">
        <div>
          <a-button type="text" @click="router.push(instancesLink())"
            ><template #icon><icon-left /></template>返回运行实例</a-button
          >
          <h2>{{ store.strategy?.name || store.instance?.strategy_id || "策略实例" }}</h2>
          <span class="mono">{{ store.instance?.instance_id || instanceId }}</span>
          <span v-if="readAt" class="muted"> · 状态读取 {{ readAt }}</span>
        </div>
        <div class="page-actions">
          <a-switch v-model="autoRefresh" size="small" aria-label="自动刷新" /><span class="muted" aria-hidden="true"
            >自动刷新</span
          ><a-button :disabled="!store.instance" @click="openReplay">回放</a-button
          ><a-button :loading="refreshing" aria-label="刷新实例详情" @click="refreshDetail"
            ><template #icon><icon-refresh /></template>刷新</a-button
          >
        </div>
      </div>
      <a-alert v-if="store.detailError" type="error" show-icon class="top-alert"
        >详情加载失败：{{ store.detailError
        }}<template #action><a-button size="small" @click="refreshDetail">重试</a-button></template></a-alert
      >
      <template v-if="store.instance">
        <a-descriptions :column="{ xs: 1, md: 2, xl: 3 }" bordered size="small" class="summary">
          <a-descriptions-item label="实例状态"
            ><StatusBadge :enabled="store.instance.enabled" :uncertain="controlUncertain" :health="store.instance.health"
          /></a-descriptions-item>
          <a-descriptions-item label="模式"
            ><a-tag :color="store.instance.logical_account_id ? 'orange' : 'blue'">{{
              store.instance.logical_account_id ? "交易模式" : "仅计算"
            }}</a-tag></a-descriptions-item
          >
          <a-descriptions-item label="策略定义"
            >{{ store.strategy?.name || definitionHint }} ·
            <span class="mono">{{ store.instance.strategy_id }}</span></a-descriptions-item
          >
          <a-descriptions-item label="View"
            ><span class="mono">{{ store.instance.view_id }}</span></a-descriptions-item
          >
          <a-descriptions-item label="运行会话"
            ><span class="mono">{{ store.instance.session_id || "无当前会话" }}</span></a-descriptions-item
          >
          <a-descriptions-item label="组合账户"
            ><a-link
              v-if="store.instance.logical_account_id"
              @click="
                router.push({
                  path: '/trading/accounts',
                  query: { view: 'strategy', logical_account_id: store.instance.logical_account_id }
                })
              "
              >{{ store.instance.logical_account_id }}</a-link
            ><span v-else>未关联账户</span></a-descriptions-item
          >
        </a-descriptions>
        <OperationPanel
          :instance-id="instanceId"
          :enabled="store.instance.enabled"
          :uncertain="controlUncertain"
          @started="handleControlStarted"
          @changed="reconcileControl"
          @failed="handleControlFailed"
        />
        <InstanceResults
          :instance="store.instance"
          :snapshot="store.targetSnapshot"
          :target-state="targetState"
          :results="store.results"
          :total="store.totalResults"
          :page="resultPage"
          :page-size="resultPageSize"
          :scope="resultScope"
          :loading="store.resultsLoading"
          :error="store.resultsError"
          :initial-loading="initialLoading"
          @page="loadResults"
          @retry="loadResults(resultPage, resultScope)"
        />
        <a-collapse :bordered="false" class="extra-sections">
          <a-collapse-item key="bindings" header="输入绑定（会话固化的 View 列与因子指纹）"
            ><ResolvedTable :resolved-json="store.instance.resolved_json"
          /></a-collapse-item>
          <a-collapse-item key="definition" header="DSL 定义">
            <pre class="code-block">{{ store.strategy?.dsl_yaml || definitionEmptyText }}</pre>
          </a-collapse-item>
        </a-collapse>
      </template>
      <a-empty v-else-if="!store.detailLoading" description="实例不存在或无法读取" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { useSpaceStore } from "@/store/modules/space";
import InstanceResults from "@/views/strategy/components/strategy-instance-results.vue";
import OperationPanel from "@/views/strategy/components/strategy-operation-panel.vue";
import ResolvedTable from "@/views/strategy/components/strategy-resolved-table.vue";
import StatusBadge from "@/views/strategy/components/strategy-status-badge.vue";
import { instancesLink, replayLink } from "@/views/strategy/links";
import { useInstanceResults } from "@/views/strategy/use-instance-results";

defineOptions({ name: "StrategyDetail" });
const route = useRoute();
const router = useRouter();
const spaceStore = useSpaceStore();
const instanceId = computed(() => String(route.params.instanceId || ""));
const controlUncertain = ref(false);
const autoRefresh = ref(true);

// 读取到的实例不属于当前空间（例如从别的空间的链接进入）：停止刷新并回到实例列表。
function leaveIfOtherSpace() {
  const currentSpaceId = spaceStore.selectedSpaceId;
  if (store.instance && currentSpaceId && store.instance.space_id !== currentSpaceId) {
    store.stopPolling();
    store.clearDetail();
    router.replace(instancesLink());
    return true;
  }
  return false;
}

const {
  store,
  resultPage,
  resultScope,
  resultPageSize,
  readAt,
  refreshing,
  initialLoading,
  targetState,
  load,
  refresh,
  loadResults,
  start
} = useInstanceResults(instanceId, {
  autoRefresh,
  controlUncertain,
  onLoaded: () => void leaveIfOtherSpace(),
  onReset: () => {
    controlUncertain.value = false;
  }
});

const definitionHint = computed(() =>
  store.strategyStatus === "missing" ? "定义已删除" : store.strategyStatus === "error" ? "定义读取失败" : "定义读取中"
);
const definitionEmptyText = computed(() =>
  store.strategyStatus === "missing"
    ? "定义已删除；历史结果的解释里保留了当时的 DSL 版本"
    : store.strategyStatus === "error"
      ? "定义读取失败（并非已删除），将在下次刷新时重试"
      : "定义读取中"
);

async function refreshDetail() {
  if (controlUncertain.value) await reconcileControl();
  else await refresh();
}
/** 启停结果未知时重新读取实例，读到最新状态后解除“状态未知”。 */
async function reconcileControl() {
  const applied = await load();
  if (applied && !store.detailError) controlUncertain.value = false;
}
function handleControlStarted() {
  controlUncertain.value = true;
}
function handleControlFailed() {
  controlUncertain.value = true;
}
function openReplay() {
  if (store.instance) router.push(replayLink({ instanceId: store.instance.instance_id, viewId: store.instance.view_id }));
}
onMounted(start);
watch(
  () => spaceStore.selectedSpaceId,
  (next, previous) => {
    if (!next || next === previous) return;
    if (!leaveIfOtherSpace() && !previous) void load();
  }
);
</script>

<style scoped>
.page-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: var(--moox-space-2);
}
.page-actions {
  display: flex;
  align-items: center;
  gap: 8px;
}
.muted {
  color: var(--color-text-3);
  font-size: 12px;
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
.extra-sections {
  margin-top: var(--moox-space-3);
}
.mono,
.code-block {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
}
.code-block {
  max-height: 620px;
  overflow: auto;
  margin: 0;
  padding: 14px;
  background: var(--color-fill-2);
  white-space: pre-wrap;
  font-size: 12px;
  line-height: 1.6;
}
@media (max-width: 640px) {
  .page-head {
    align-items: flex-start;
    flex-direction: column;
  }
  .page-actions {
    width: 100%;
    justify-content: flex-end;
  }
}
</style>
