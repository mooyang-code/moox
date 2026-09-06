<template>
  <div class="moox-page"><div class="moox-inner">
    <div class="page-head"><div><a-button type="text" @click="router.push({ name: 'strategy-running' })"><template #icon><icon-left /></template>返回实例</a-button><h2>{{ store.instance?.instance_id || instanceId }}</h2><span>{{ store.strategy?.name || store.instance?.strategy_id || "策略实例" }}</span></div><div class="page-actions"><a-switch v-model="autoRefresh" size="small" /><span class="muted">自动刷新</span><a-button :loading="store.detailLoading" aria-label="刷新实例详情" @click="refresh"><template #icon><icon-refresh /></template>刷新</a-button></div></div>
    <a-alert v-if="store.detailError" type="error" show-icon class="top-alert">详情加载失败：{{ store.detailError }}<template #action><a-button size="small" @click="refresh">重试</a-button></template></a-alert>
    <template v-if="store.instance">
      <a-descriptions :column="{ xs: 1, sm: 3 }" bordered class="summary"><a-descriptions-item label="实例状态"><StatusBadge :enabled="store.instance.enabled" :uncertain="controlUncertain" /></a-descriptions-item><a-descriptions-item label="模式"><a-tag :color="store.instance.logical_account_id ? 'orange' : 'blue'">{{ store.instance.logical_account_id ? "交易模式" : "仅计算" }}</a-tag></a-descriptions-item><a-descriptions-item label="策略 ID">{{ store.instance.strategy_id }}</a-descriptions-item><a-descriptions-item label="运行会话"><span class="mono">{{ store.instance.session_id || "无当前会话" }}</span></a-descriptions-item><a-descriptions-item label="逻辑账户"><a-link v-if="store.instance.logical_account_id" @click="router.push({ name: 'trading-logical-accounts', query: { logical_account_id: store.instance.logical_account_id } })">{{ store.instance.logical_account_id }}</a-link><span v-else>未关联账户</span></a-descriptions-item><a-descriptions-item label="更新时间">{{ formatStrategyTime(store.instance.updated_at) }}</a-descriptions-item></a-descriptions>
      <a-alert v-if="!store.instance.enabled && store.instance.session_id" type="warning" show-icon class="top-alert">实例已经停用，但会话仍存在。后台账户释放或控制操作可能尚未完成，请不要将旧目标当作当前有效仓位。</a-alert>
      <OperationPanel :instance-id="instanceId" :enabled="store.instance.enabled" :uncertain="controlUncertain" @started="handleControlStarted" @changed="reconcileControl" @failed="handleControlFailed" />
      <a-tabs v-model:active-key="activeTab">
        <a-tab-pane key="targets" title="最新目标"><TargetTable :targets="store.targetSnapshot?.targets || []" :snapshot="store.targetSnapshot" :state="targetState" /></a-tab-pane>
        <a-tab-pane key="results" title="历史结果"><ResultTimeline :results="store.results" :total="store.totalResults" :page="resultPage" :page-size="resultPageSize" :scope="resultScope" @page="loadResults" /></a-tab-pane>
        <a-tab-pane key="bindings" title="输入绑定"><pre class="code-block">{{ prettyBindings }}</pre></a-tab-pane>
        <a-tab-pane key="definition" title="DSL 定义"><pre class="code-block">{{ store.strategy?.dsl_yaml || "{}" }}</pre></a-tab-pane>
      </a-tabs>
    </template>
    <a-empty v-else-if="!store.detailLoading" description="实例不存在或无法读取" />
  </div></div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { useSpaceStore } from "@/store/modules/space";
import { useStrategyStore } from "@/store/modules/strategy";
import OperationPanel from "@/views/strategy/components/strategy-operation-panel.vue";
import ResultTimeline from "@/views/strategy/components/strategy-run-timeline.vue";
import StatusBadge from "@/views/strategy/components/strategy-status-badge.vue";
import TargetTable from "@/views/strategy/components/strategy-target-table.vue";
import { deriveTargetState, formatStrategyTime } from "@/views/strategy/model";

defineOptions({ name: "StrategyDetail" });
const route = useRoute(); const router = useRouter(); const store = useStrategyStore(); const spaceStore = useSpaceStore(); const instanceId = computed(() => String(route.params.instanceId || "")); const activeTab = ref("targets"); const resultPage = ref(1); const resultPageSize = 20; const resultScope = ref<"session" | "all">("session"); const controlUncertain = ref(false); const autoRefresh = ref(true);
const targetState = computed(() => store.instance && !controlUncertain.value ? deriveTargetState(store.instance, store.targetSnapshot) : "unknown");
const prettyBindings = computed(() => { try { return JSON.stringify(JSON.parse(store.instance?.input_bindings_json || "{}"), null, 2); } catch { return store.instance?.input_bindings_json || "{}"; } });
async function load(reconcileControlState = false) { try { const applied = await store.loadInstanceDetail(instanceId.value, resultPage.value, resultPageSize, resultScope.value === "all"); const currentSpaceId = spaceStore.selectedSpaceId; if (store.instance && currentSpaceId && store.instance.space_id !== currentSpaceId) { store.stopPolling(); store.clearDetail(); router.replace({ name: "strategy-running" }); return; } if (reconcileControlState && applied && !store.detailError) controlUncertain.value = false; } catch { /* error is rendered by the store */ } }
async function refresh() { await load(controlUncertain.value); }
async function loadResults(page: number, scope: "session" | "all") { resultPage.value = page; resultScope.value = scope; await store.loadResultPage(page, resultPageSize, scope === "all"); }
async function reconcileControl() { await load(true); }
function handleControlStarted() { controlUncertain.value = true; }
function handleControlFailed() { controlUncertain.value = true; }
onMounted(() => { load(); store.startPolling(() => autoRefresh.value ? load() : undefined); });
watch(() => route.params.instanceId, (next, previous) => { if (next !== previous) { store.stopPolling(); store.clearDetail(); resultPage.value = 1; resultScope.value = "session"; controlUncertain.value = false; load(); store.startPolling(() => autoRefresh.value ? load() : undefined); } });
watch(() => spaceStore.selectedSpaceId, (next, previous) => { if (next && next !== previous) { if (store.instance && store.instance.space_id !== next) { store.stopPolling(); store.clearDetail(); router.replace({ name: "strategy-running" }); } else if (!previous) load(); } });
onUnmounted(() => { store.stopPolling(); store.clearDetail(); });
</script>

<style scoped>
.page-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; margin-bottom: var(--moox-space-2); }
.page-actions { display: flex; align-items: center; gap: 8px; }
.muted { color: var(--color-text-3); font-size: 12px; }
.page-head h2 { margin: 8px 0 4px; }
.page-head span { color: var(--color-text-3); }
.summary, .top-alert { margin-bottom: var(--moox-space-2); }
.mono, .code-block { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
.code-block { max-height: 620px; overflow: auto; margin: 0; padding: 14px; background: var(--color-fill-2); white-space: pre-wrap; font-size: 12px; line-height: 1.6; }
@media (max-width: 640px) { .page-head { align-items: flex-start; flex-direction: column; } .page-actions { width: 100%; justify-content: flex-end; } }
</style>
