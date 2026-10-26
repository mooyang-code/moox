<template>
  <div class="moox-page">
    <div class="moox-inner">
      <a-alert v-if="store.instancesError" type="error" show-icon class="top-alert"
        >实例加载失败：{{ store.instancesError
        }}<template #action><a-button size="small" @click="refresh">重试</a-button></template></a-alert
      >
      <a-alert v-if="store.catalogError" type="warning" show-icon class="top-alert"
        >策略定义目录加载失败，定义名称与筛选项可能不完整：{{ store.catalogError
        }}<template #action><a-button size="small" @click="loadCatalog">重试</a-button></template></a-alert
      >
      <div class="result-toolbar">
        <span class="result-count">
          共 {{ store.totalInstances }} 个实例<span v-if="readAt">；状态读取：{{ readAt }}</span>
          <InfoTip
            text="实例绑定一个定义、一个 View 和可选的组合账户；启用后由 View 就绪事件驱动计算，结果在「运行结果」查看。"
          />
        </span>
        <a-space wrap>
          <a-select v-model="filters.strategy_id" allow-clear allow-search placeholder="全部策略定义" class="filter-select"
            ><a-option v-for="item in store.strategyCatalog" :key="item.strategy_id" :value="item.strategy_id"
              >{{ item.name }}（{{ item.strategy_id }}）</a-option
            ></a-select
          >
          <a-select v-model="filters.enabled" allow-clear placeholder="全部状态" class="filter-status"
            ><a-option value="true">已启用</a-option><a-option value="false">已停用</a-option></a-select
          >
          <a-button @click="search">查询</a-button>
          <a-button @click="reset">重置</a-button>
          <a-button :loading="store.instancesLoading" aria-label="刷新实例" @click="refresh"
            ><template #icon><icon-refresh /></template>刷新</a-button
          >
          <a-button type="primary" status="success" :disabled="!spaceStore.selectedSpaceId" @click="openCreate"
            ><template #icon><icon-plus /></template>新建实例</a-button
          >
        </a-space>
      </div>
      <a-table
        row-key="instance_id"
        :data="store.instances"
        :loading="store.instancesLoading"
        :pagination="pagination"
        :scroll="{ x: 940 }"
        @page-change="changePage"
      >
        <template #columns>
          <a-table-column title="实例" :width="220"
            ><template #cell="{ record }"
              ><a-link @click="openQuick(record.instance_id)">{{ strategyName(record.strategy_id) }}</a-link>
              <div class="cell-sub mono">{{ record.instance_id }}</div></template
            ></a-table-column
          >
          <a-table-column title="因子任务" :width="200" :ellipsis="true" :tooltip="true"
            ><template #cell="{ record }"
              ><span class="mono">{{ record.factor_task_id }}</span></template
            ></a-table-column
          >
          <a-table-column title="模式" :width="80"
            ><template #cell="{ record }"
              ><a-tag :color="record.logical_account_id ? 'orange' : 'blue'">{{
                record.logical_account_id ? "交易模式" : "仅计算"
              }}</a-tag></template
            ></a-table-column
          >
          <a-table-column title="状态" :width="110"
            ><template #cell="{ record }"><StatusBadge :enabled="record.enabled" :health="record.health" /></template
          ></a-table-column>
          <a-table-column title="更新时间" :width="180"
            ><template #cell="{ record }">{{ formatStrategyTime(record.updated_at) }}</template></a-table-column
          >
          <a-table-column title="操作" :width="170" fixed="right">
            <template #cell="{ record }">
              <div class="row-actions">
                <a-button type="text" size="mini" @click="openResults(record.instance_id)">结果</a-button>
                <a-button type="text" size="mini" @click="openDetail(record.instance_id)">详情</a-button>
                <a-dropdown trigger="click">
                  <a-button type="text" size="mini">更多</a-button>
                  <template #content>
                    <a-doption :disabled="togglingId === record.instance_id" @click="toggle(record)">{{
                      record.enabled ? "停用实例" : "启用实例"
                    }}</a-doption>
                  </template>
                </a-dropdown>
              </div>
            </template>
          </a-table-column>
        </template>
        <template #empty>
          <a-empty v-if="empty === 'error'" description="读取失败，暂无可显示的实例" />
          <a-empty v-else-if="empty === 'no-match'" description="没有符合筛选条件的实例">
            <a-button @click="reset">清除筛选</a-button>
          </a-empty>
          <a-empty v-else description="当前空间暂无策略实例">
            <a-button type="primary" status="success" :disabled="!spaceStore.selectedSpaceId" @click="openCreate"
              >新建实例</a-button
            >
          </a-empty>
        </template>
      </a-table>
      <InstanceQuickDrawer
        :instance-id="state.instanceId"
        :strategies="store.strategyCatalog"
        @close="closeOverlay"
        @open-detail="openDetail"
        @results="openResults"
        @replay="goReplay"
      />
      <StrategyInstanceCreate
        :visible="state.create"
        :strategies="store.strategyCatalog"
        :space-id="spaceStore.selectedSpaceId"
        :preset-strategy-id="state.strategyId"
        @update:visible="value => (value ? openCreate() : closeOverlay())"
        @created="created"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import type { Strategy, StrategyInstance } from "@/api/strategy-types";
import { useSpaceStore } from "@/store/modules/space";
import { useStrategyStore } from "@/store/modules/strategy";
import StatusBadge from "@/views/strategy/components/strategy-status-badge.vue";
import StrategyInstanceCreate from "@/views/strategy/components/strategy-instance-create.vue";
import { confirmInstanceToggle } from "@/views/strategy/components/instance-toggle";
import { formatStrategyTime } from "@/views/strategy/model";
import InfoTip from "@/views/factor/shared/info-tip.vue";
import { STRATEGY_TASKS_ROUTE, detailLink, normalizeTaskTab, replayLink, resultsLink } from "@/views/strategy/links";
import InstanceQuickDrawer from "@/views/strategy/running/instance-quick-drawer.vue";
import {
  buildRunningQuery,
  emptyKind,
  listKey,
  parseRunningQuery,
  type RunningState
} from "@/views/strategy/running/running-model";

defineOptions({ name: "StrategyRunning" });
const ROUTE_NAME = STRATEGY_TASKS_ROUTE;
const PAGE_SIZE = 20;
const router = useRouter();
const route = useRoute();
const store = useStrategyStore();
const spaceStore = useSpaceStore();
// URL 是已应用筛选、分页、快速详情与创建抽屉的唯一来源；filters 只是筛选框里还没点“查询”的草稿。
const state = computed<RunningState>(() => parseRunningQuery(route.query));
const filters = reactive({ strategy_id: state.value.strategyId, enabled: state.value.enabled as string });
const pagination = computed(() => ({ current: state.value.page, pageSize: PAGE_SIZE, total: store.totalInstances }));
const readAt = ref("");
const togglingId = ref("");
const empty = computed(() =>
  emptyKind({
    loading: store.instancesLoading,
    error: store.instancesError,
    count: store.instances.length,
    filtered: Boolean(state.value.strategyId || state.value.enabled)
  })
);
let loadedKey = "";

function go(next: Partial<RunningState>, mode: "push" | "replace" = "replace") {
  const target = { name: ROUTE_NAME, query: buildRunningQuery({ ...state.value, ...next }) };
  return mode === "push" ? router.push(target) : router.replace(target);
}
async function refresh() {
  loadedKey = listKey(state.value);
  const { strategyId, enabled, page } = state.value;
  try {
    await store.loadInstances({
      strategy_id: strategyId || undefined,
      enabled: enabled === "" ? undefined : enabled === "true",
      page,
      page_size: PAGE_SIZE
    });
    readAt.value = new Date().toLocaleTimeString("zh-CN", { hour12: false });
  } catch {
    // 错误已记入 store.instancesError，页面就近展示。
  }
}
// 定义目录只用于名称与筛选项：它失败不应让整张实例表显示为读取失败。
async function loadCatalog() {
  if (store.strategiesComplete) return;
  await store.loadAllStrategies(200).catch(() => undefined);
}
function search() {
  go({
    strategyId: filters.strategy_id || "",
    enabled: (filters.enabled || "") as RunningState["enabled"],
    page: 1,
    instanceId: ""
  });
}
function reset() {
  filters.strategy_id = "";
  filters.enabled = "";
  go({ strategyId: "", enabled: "", page: 1, instanceId: "" });
}
function changePage(page: number) {
  go({ page, instanceId: "" });
}
function openQuick(instanceId: string) {
  go({ instanceId, create: false }, "push");
}
function openCreate() {
  if (!state.value.create) go({ create: true, instanceId: "" }, "push");
}
function closeOverlay() {
  go({ instanceId: "", create: false });
}
function openDetail(instanceId: string) {
  router.push(detailLink(instanceId));
}
function openResults(instanceId: string) {
  router.push(resultsLink(instanceId));
}
function goReplay(instance: StrategyInstance) {
  router.push(replayLink({ instanceId: instance.instance_id, factorTaskId: instance.factor_task_id }));
}
/** 本 Tab 是否是当前页面：离开工作台或切到其它 Tab 后不再响应 URL。 */
function isCurrentTab() {
  return route.name === ROUTE_NAME && normalizeTaskTab(route.query.tab) === "instances";
}
function strategyName(id: string) {
  return store.strategyCatalog.find((item: Strategy) => item.strategy_id === id)?.name || "定义已删除或未加载";
}
function created(instanceId: string) {
  openDetail(instanceId);
}
/** 列表行启停：与详情页共用确认对话框与请求流程，结束后（无论成败）重新读取列表以显示真实状态。 */
function toggle(instance: StrategyInstance) {
  if (togglingId.value) return;
  confirmInstanceToggle(instance.instance_id, !instance.enabled, {
    onStart: () => (togglingId.value = instance.instance_id),
    onSettled: async () => {
      await refresh();
      togglingId.value = "";
    }
  });
}
onMounted(() => {
  // 进入页面先清掉 store 里可能属于其它空间的实例：期间在别的页面切过空间时，请求失败也不能继续显示旧空间的实例。
  store.clearInstances();
  if (spaceStore.selectedSpaceId) void Promise.all([refresh(), loadCatalog()]);
});
// 空间切换的代次：快速连续切换时，较早的切换流程在等待导航之后发现自己已过期就退出，由最新一次负责读取。
let spaceGeneration = 0;
// 空间初始化（无 → 有）只是首次加载，不能清掉深链接参数；真正的 A → B 切换才清空旧空间的实例与选中对象。
watch(
  () => spaceStore.selectedSpaceId,
  async (next, previous) => {
    if (!next) return;
    const generation = ++spaceGeneration;
    if (previous) {
      store.clearInstances();
      // 先记下新条件对应的键再改 URL：路由监听据此跳过重复读取，且显式读取用的是已更新的页码。
      const cleared = { ...state.value, page: 1, instanceId: "", create: false };
      loadedKey = listKey(cleared);
      await go(cleared);
      if (generation !== spaceGeneration) return;
    }
    void Promise.all([refresh(), loadCatalog()]);
  }
);
// URL 变化（查询、翻页、前进后退）：同步草稿；只有列表条件变了才重新读取，打开抽屉不触发读取。
watch(
  () => listKey(state.value),
  key => {
    if (!isCurrentTab()) return;
    filters.strategy_id = state.value.strategyId;
    filters.enabled = state.value.enabled;
    if (key !== loadedKey && spaceStore.selectedSpaceId) void refresh();
  }
);
</script>

<style scoped lang="scss">
@use "../shared/strategy-page.scss";

.muted {
  color: var(--color-text-3);
  font-size: 12px;
}
.top-alert {
  margin-bottom: var(--moox-space-2);
}
.filter-select {
  width: 240px;
}
.filter-status {
  width: 140px;
}
@media (max-width: 640px) {
  .filter-select,
  .filter-status {
    width: 100%;
  }
}
.row-actions {
  display: flex;
  flex-wrap: nowrap;
  align-items: center;
  gap: 2px;
}
</style>
