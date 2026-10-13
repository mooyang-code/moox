<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="page-head">
        <div>
          <h2>策略定义</h2>
          <span>DSL 是唯一策略配置来源，保存定义不会启用任何实例。</span>
        </div>
        <a-space>
          <a-button :loading="store.listLoading" aria-label="刷新策略定义" @click="refresh"
            ><template #icon><icon-refresh /></template>刷新</a-button
          >
          <a-button type="primary" status="success" @click="router.push({ name: 'strategy-definition-new' })"
            ><template #icon><icon-plus /></template>新建策略</a-button
          >
        </a-space>
      </div>
      <a-alert v-if="store.error" type="error" show-icon class="top-alert"
        >策略定义加载失败：{{ store.error
        }}<template #action><a-button size="small" @click="refresh">重试</a-button></template></a-alert
      >
      <div class="toolbar muted">共 {{ store.totalStrategies }} 条</div>
      <a-table
        row-key="strategy_id"
        :data="store.strategies"
        :loading="store.listLoading"
        :pagination="pagination"
        :scroll="{ x: 900 }"
        @page-change="changePage"
      >
        <template #columns>
          <a-table-column title="策略名称" :width="220"
            ><template #cell="{ record }"
              ><a-link @click="openDetail(record.strategy_id)">{{ record.name || "未命名策略" }}</a-link>
              <div class="muted mono">{{ record.strategy_id }}</div></template
            ></a-table-column
          >
          <a-table-column title="DSL 摘要"
            ><template #cell="{ record }"
              ><span class="summary">{{ summarizeDSL(record.dsl_yaml) }}</span></template
            ></a-table-column
          >
          <a-table-column title="版本" :width="110"
            ><template #cell="{ record }"
              ><a-tooltip :content="record.dsl_hash"
                ><span class="mono">{{ shortHash(record.dsl_hash) }}</span></a-tooltip
              ></template
            ></a-table-column
          >
          <a-table-column title="更新时间" :width="170"
            ><template #cell="{ record }">{{
              formatStrategyTime(record.updated_at || record.created_at)
            }}</template></a-table-column
          >
          <a-table-column title="操作" :width="170" fixed="right">
            <template #cell="{ record }">
              <div class="row-actions">
                <a-button type="text" size="mini" @click="openDetail(record.strategy_id)">查看</a-button>
                <a-button type="text" size="mini" @click="edit(record.strategy_id)">编辑</a-button>
                <a-dropdown trigger="click">
                  <a-button type="text" size="mini">更多</a-button>
                  <template #content>
                    <a-doption @click="goReplay(record.strategy_id)">发起回放</a-doption>
                    <a-doption @click="confirmRemove(record)">删除</a-doption>
                  </template>
                </a-dropdown>
              </div>
            </template>
          </a-table-column>
        </template>
        <template #empty>
          <a-empty v-if="store.error" description="读取失败，暂无可显示的定义" />
          <a-empty v-else description="暂无策略定义">
            <a-button type="primary" status="success" @click="router.push({ name: 'strategy-definition-new' })"
              >新建策略</a-button
            >
          </a-empty>
        </template>
      </a-table>
      <StrategyDefinitionDrawer
        :definition-id="detailId"
        @close="closeDetail"
        @edit="edit"
        @instances="goInstances"
        @create-instance="goCreateInstance"
        @replay="goReplay"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, watch } from "vue";
import { Message, Modal } from "@arco-design/web-vue";
import { useRoute, useRouter } from "vue-router";
import { useSpaceStore } from "@/store/modules/space";
import { useStrategyStore } from "@/store/modules/strategy";
import type { Strategy } from "@/api/strategy-types";
import { summarizeDSL } from "@/views/strategy/dsl";
import { formatStrategyTime, shortHash } from "@/views/strategy/model";
import StrategyDefinitionDrawer from "@/views/strategy/definitions/strategy-definition-drawer.vue";
import { instancesLink, replayLink } from "@/views/strategy/links";
import { buildQuery, queryPage, queryString } from "@/views/strategy/query";

defineOptions({ name: "StrategyDefinitions" });
const ROUTE_NAME = "strategy-definitions";
const router = useRouter();
const route = useRoute();
const store = useStrategyStore();
const spaceStore = useSpaceStore();
// URL 是分页与查看对象的唯一来源：刷新、前进后退都能恢复；定义是全局资源，切空间时保留。
const pagination = reactive({ current: queryPage(route.query.page), pageSize: 20, total: computed(() => store.totalStrategies) });
const detailId = computed(() => queryString(route.query.definition_id));

function stateQuery(patch: { page?: number; definition_id?: string }) {
  return buildQuery(
    { page: patch.page ?? pagination.current, definition_id: "definition_id" in patch ? patch.definition_id : detailId.value },
    { page: 1 }
  );
}
async function refresh() {
  await store.loadStrategies({ page: pagination.current, page_size: pagination.pageSize }).catch(() => undefined);
}
function changePage(page: number) {
  void router.replace({ name: ROUTE_NAME, query: stateQuery({ page }) });
}
function openDetail(strategyId: string) {
  void router.push({ name: ROUTE_NAME, query: stateQuery({ definition_id: strategyId }) });
}
function closeDetail() {
  void router.replace({ name: ROUTE_NAME, query: stateQuery({ definition_id: "" }) });
}
function edit(strategyId: string) {
  router.push({ name: "strategy-definition-edit", params: { strategyId } });
}
function goInstances(strategyId: string) {
  router.push(instancesLink({ strategyId }));
}
function goCreateInstance(strategyId: string) {
  router.push(instancesLink({ strategyId, create: true }));
}
function goReplay(strategyId: string) {
  router.push(replayLink({ strategyId }));
}
function confirmRemove(record: Strategy) {
  Modal.warning({
    title: `删除策略定义「${record.name || record.strategy_id}」`,
    content: "删除后定义从列表消失，已有会话与历史结果保留。仍被实例引用的定义不能删除。确认删除？",
    hideCancel: false,
    okButtonProps: { status: "danger" },
    onOk: () => remove(record.strategy_id)
  });
}
async function remove(strategyId: string) {
  try {
    await store.removeStrategy(strategyId);
    Message.success("策略定义已删除");
  } catch (err) {
    Message.error(err instanceof Error ? err.message : "删除失败");
  }
}
// 请求按空间授权：空间初始化完成或切换后才加载，避免无空间头被拒绝且不再恢复。
onMounted(() => {
  if (spaceStore.selectedSpaceId) void refresh();
});
watch(
  () => spaceStore.selectedSpaceId,
  next => {
    if (next) void refresh();
  }
);
// 页码变化来自 URL（翻页、前进后退）；离开本页时路由已切换，不再响应。
watch(
  () => queryPage(route.query.page),
  page => {
    if (route.name !== ROUTE_NAME || page === pagination.current) return;
    pagination.current = page;
    if (spaceStore.selectedSpaceId) void refresh();
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
.page-head h2 {
  margin: 0 0 4px;
}
.page-head span,
.muted {
  color: var(--color-text-3);
  font-size: 12px;
}
.top-alert {
  margin-bottom: var(--moox-space-2);
}
.toolbar {
  margin-bottom: 8px;
}
.summary {
  color: var(--color-text-2);
}
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
}
@media (max-width: 640px) {
  .page-head {
    align-items: flex-start;
    flex-direction: column;
  }
}
.row-actions {
  display: flex;
  flex-wrap: nowrap;
  align-items: center;
  gap: 2px;
}
</style>
