<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="page-head">
        <div>
          <h2>策略定义</h2>
          <span>DSL 是唯一策略配置来源，保存定义不会启用任何实例。</span>
        </div>
        <a-space>
          <a-button :loading="listLoading" aria-label="刷新策略定义" @click="refresh"
            ><template #icon><icon-refresh /></template>刷新</a-button
          >
          <a-button type="primary" status="success" @click="templateVisible = true">从模板新建</a-button>
          <a-button type="primary" status="success" @click="router.push({ name: 'strategy-definition-new' })"
            ><template #icon><icon-plus /></template>新建策略</a-button
          >
        </a-space>
      </div>
      <a-alert v-if="listError" type="error" show-icon class="top-alert"
        >策略定义加载失败：{{ listError
        }}<template #action><a-button size="small" @click="refresh">重试</a-button></template></a-alert
      >
      <div class="toolbar">
        <a-input-search
          v-model="keywordDraft"
          allow-clear
          placeholder="搜索策略名称或 ID，回车搜索"
          class="definition-search"
          @search="search"
          @press-enter="search"
          @clear="search"
        />
        <span class="muted">共 {{ pagination.total }} 条</span>
      </div>
      <a-table
        row-key="strategy_id"
        :data="rows"
        :loading="listLoading"
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
              formatInstant(record.updated_at || record.created_at, displayZone)
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
          <a-empty v-if="listError" description="读取失败，暂无可显示的定义" />
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
      <StrategyTemplateCreate :visible="templateVisible" @close="templateVisible = false" @created="onTemplateCreated" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from "vue";
import { Message, Modal } from "@arco-design/web-vue";
import { useRoute, useRouter } from "vue-router";
import { useSpaceStore } from "@/store/modules/space";
import { useStrategyStore } from "@/store/modules/strategy";
import type { Strategy } from "@/api/strategy-types";
import { summarizeDSL } from "@/views/strategy/dsl";
import { shortHash } from "@/views/strategy/model";
import StrategyDefinitionDrawer from "@/views/strategy/definitions/strategy-definition-drawer.vue";
import StrategyTemplateCreate from "@/views/strategy/definitions/strategy-template-create.vue";
import { instancesLink, replayLink } from "@/views/strategy/links";
import { buildQuery, queryPage, queryString } from "@/views/strategy/query";
import { formatInstant } from "@/utils/time-zone";
import { useDisplayZone } from "@/hooks/useDisplayZone";

// 页面时间按当前空间登记的时区显示
const displayZone = useDisplayZone();

defineOptions({ name: "StrategyDefinitions" });
const ROUTE_NAME = "strategy-definitions";
const router = useRouter();
const route = useRoute();
const store = useStrategyStore();
const spaceStore = useSpaceStore();
// URL 是分页与查看对象的唯一来源：刷新、前进后退都能恢复；定义是全局资源，切空间时保留。
// 搜索词进 URL（?q=）：后端没有按名称搜索，搜索时在完整的策略目录里筛，前端分页；不搜索时按后端分页。
const keyword = computed(() => queryString(route.query.q).trim());
const keywordDraft = ref(keyword.value);
const searching = computed(() => Boolean(keyword.value));
const matched = computed(() => {
  const text = keyword.value.toLowerCase();
  return store.strategyCatalog.filter(
    item => item.strategy_id.toLowerCase().includes(text) || (item.name || "").toLowerCase().includes(text)
  );
});
const pagination = reactive({
  current: queryPage(route.query.page),
  pageSize: 20,
  total: computed(() => (searching.value ? matched.value.length : store.totalStrategies))
});
const rows = computed(() =>
  searching.value
    ? matched.value.slice((pagination.current - 1) * pagination.pageSize, pagination.current * pagination.pageSize)
    : store.strategies
);
// 搜索时读的是完整目录，错误和加载态也看目录那一份
const listError = computed(() => (searching.value ? store.catalogError : store.error));
const listLoading = computed(() => (searching.value ? store.catalogLoading : store.listLoading));
const detailId = computed(() => queryString(route.query.definition_id));

function stateQuery(patch: { page?: number; definition_id?: string; q?: string }) {
  return buildQuery(
    {
      page: patch.page ?? pagination.current,
      definition_id: "definition_id" in patch ? patch.definition_id : detailId.value,
      q: "q" in patch ? patch.q : keyword.value
    },
    { page: 1 }
  );
}
async function refresh() {
  if (searching.value) await store.loadAllStrategies(200).catch(() => undefined);
  else await store.loadStrategies({ page: pagination.current, page_size: pagination.pageSize }).catch(() => undefined);
}
function search() {
  void router.replace({ name: ROUTE_NAME, query: stateQuery({ q: keywordDraft.value.trim(), page: 1 }) });
}
function changePage(page: number) {
  void router.replace({ name: ROUTE_NAME, query: stateQuery({ page }) });
}
function openDetail(strategyId: string) {
  void router.push({ name: ROUTE_NAME, query: stateQuery({ definition_id: strategyId }) });
}
const templateVisible = ref(false);
// 从模板生成的策略是普通定义：创建后打开编辑页，可以继续改 DSL。
function onTemplateCreated(strategyId: string) {
  templateVisible.value = false;
  void refresh();
  edit(strategyId);
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
    return;
  }
  // 删掉后重读本页补齐；末页删空了回到上一页
  const lastPage = Math.max(1, Math.ceil(pagination.total / pagination.pageSize));
  if (pagination.current > lastPage) changePage(lastPage);
  else await refresh();
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
    if (spaceStore.selectedSpaceId && !searching.value) void refresh();
  }
);
// 搜索词变化（搜索、清空、前进后退）：同步输入框；开始搜索时读完整目录
watch(keyword, (next, previous) => {
  if (route.name !== ROUTE_NAME) return;
  keywordDraft.value = next;
  if (next && !previous && !store.strategiesComplete && spaceStore.selectedSpaceId) void refresh();
  if (!next && previous && spaceStore.selectedSpaceId) void refresh();
});
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
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 12px;
  margin-bottom: 8px;
}
.definition-search {
  width: 280px;
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
