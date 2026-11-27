<template>
  <div class="output-fields-tab">
    <div class="toolbar">
      <div class="toolbar-main">
        <a-button class="mobile-category-trigger" @click="mobileCategoryVisible = true">
          <template #icon><icon-menu /></template>
          因子分类
        </a-button>
        <div class="keyword-control">
          <a-input-search
            v-model="searchKeyword"
            class="keyword-input"
            allow-clear
            placeholder="搜索结果列名、因子 ID/名称、成员键或任务"
            @input="scheduleSearch"
            @search="commitSearch"
            @clear="commitSearch"
          />
        </div>
        <div class="filter-control filter-control--task">
          <a-select
            :model-value="filter.taskId || undefined"
            class="filter-select"
            allow-clear
            placeholder="计算任务"
            @change="onTaskChange"
          >
            <a-option v-for="task in taskOptions" :key="task.task_id" :value="task.task_id">{{ task.label }}</a-option>
          </a-select>
        </div>
        <div class="filter-control">
          <a-select
            :model-value="filter.kind || undefined"
            class="filter-select"
            allow-clear
            placeholder="参数模式"
            @change="onKindChange"
          >
            <a-option value="fixed">固定参数</a-option>
            <a-option value="var">可变参数</a-option>
          </a-select>
        </div>
        <div class="filter-control">
          <a-select
            :model-value="filter.status || undefined"
            class="filter-select"
            allow-clear
            placeholder="成员状态"
            @change="onStatusChange"
          >
            <a-option value="enabled">已启用</a-option>
            <a-option value="disabled">已停用</a-option>
          </a-select>
        </div>
      </div>
      <div class="toolbar-meta">
        <span class="field-total">共 {{ filteredRows.length }} 个产出字段</span>
      </div>
    </div>

    <a-alert class="output-fields-alert" type="info" show-icon>
      展示当前空间各计算任务实际产出的因子结果列名（可直接复制到策略 DSL 中引用，只读）；未加入计算任务的因子不会生成结果列。
    </a-alert>

    <a-alert v-if="loadError" class="output-fields-error" type="error" show-icon>
      计算任务加载失败：{{ loadError }} <a-link @click="emit('retryTasks')">重试</a-link>
    </a-alert>

    <section class="output-fields-workbench">
      <aside class="category-panel">
        <GroupFilterPanel
          title="因子分类"
          all-label="全部产出字段"
          :items="categoryItems"
          :total="filteredWithoutCategory.length"
          :selected="filter.category"
          :loading="loading"
          empty-text="暂无产出字段"
          @select="selectCategory"
        />
      </aside>

      <main class="output-fields-main">
        <a-table
          row-key="row_key"
          size="small"
          class="output-fields-table"
          :bordered="{ cell: true }"
          :loading="loading"
          :data="filteredRows"
          :pagination="pagination"
          :scroll="{ x: 1180, y: 'calc(100vh - 360px)' }"
          @page-change="pagination.current = $event"
          @page-size-change="onPageSizeChange"
        >
          <template #columns>
            <a-table-column
              title="结果列名"
              data-index="column_name"
              :width="240"
              :sortable="{ sortDirections: ['ascend', 'descend'] }"
            >
              <template #cell="{ record }">
                <div class="column-name-cell">
                  <div class="column-name">
                    <code :title="record.column_name">{{ record.column_name }}</code>
                    <span v-if="record.output_label" class="output-sub">{{ record.output_label }}</span>
                  </div>
                  <a-tooltip content="复制结果列名">
                    <a-button
                      class="copy-btn"
                      size="mini"
                      type="text"
                      :aria-label="`复制列名 ${record.column_name}`"
                      @click.stop="copyText(record.column_name)"
                    >
                      <template #icon><icon-copy /></template>
                    </a-button>
                  </a-tooltip>
                </div>
              </template>
            </a-table-column>

            <a-table-column title="所属因子" :width="200">
              <template #cell="{ record }">
                <div class="factor-cell">
                  <a-link @click="emit('openDetail', record.factor_id)">{{ record.factor_name }}</a-link>
                  <span v-if="record.factor_name !== record.factor_id" class="muted mono">{{ record.factor_id }}</span>
                </div>
              </template>
            </a-table-column>

            <a-table-column title="分类 / 类型" :width="140">
              <template #cell="{ record }">
                <span>{{ record.category_name }}</span>
                <span class="muted"> · {{ factorTypeLabel(record.factor_type) }}</span>
              </template>
            </a-table-column>

            <a-table-column title="参数模式与参数" :width="200">
              <template #cell="{ record }">
                <div class="params-cell">
                  <a-tag size="small" :color="record.member_kind === 'var' ? 'purple' : 'gray'">
                    {{ record.member_kind === "var" ? "可变" : "固定" }}
                  </a-tag>
                  <span class="params-text" :title="record.params_text || '默认'">{{ record.params_text || "默认" }}</span>
                </div>
              </template>
            </a-table-column>

            <a-table-column title="所属计算任务" :width="210">
              <template #cell="{ record }">
                <div class="task-cell">
                  <a-link @click="emit('openTask', record.task_id)">{{ record.task_label }}</a-link>
                  <span class="muted mono" :title="record.result_dataset_id">{{ record.result_dataset_id }}</span>
                </div>
              </template>
            </a-table-column>

            <a-table-column title="状态" :width="84">
              <template #cell="{ record }">
                <a-tag size="small" :color="memberStatusTag(record.member_status).color">
                  {{ memberStatusTag(record.member_status).label }}
                </a-tag>
              </template>
            </a-table-column>

            <a-table-column title="依赖输入列" :width="170" :ellipsis="true" :tooltip="true">
              <template #cell="{ record }">
                <span>{{ record.input_columns.join(", ") || "—" }}</span>
                <span v-if="record.lookback_periods > 0" class="muted">（回看 {{ record.lookback_periods }}）</span>
              </template>
            </a-table-column>
          </template>

          <template #empty>
            <a-empty :description="emptyText">
              <a-button v-if="!loading && !loadError && !allRows.length" size="small" type="primary" @click="goTasks">
                前往计算任务
              </a-button>
            </a-empty>
          </template>
        </a-table>
      </main>
    </section>

    <a-drawer v-model:visible="mobileCategoryVisible" title="因子分类" width="280px" :footer="false">
      <GroupFilterPanel
        title="因子分类"
        all-label="全部产出字段"
        :items="categoryItems"
        :total="filteredWithoutCategory.length"
        :selected="filter.category"
        :loading="loading"
        empty-text="暂无产出字段"
        @select="selectCategoryFromMobile"
      />
    </a-drawer>
  </div>
</template>

<script setup lang="ts">
import { computed, onActivated, onBeforeUnmount, onDeactivated, reactive, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import type { FactorCategory, FactorTask, FactorTaskInfo, MemberKind, MemberStatus } from "@/api/factor/types";
import GroupFilterPanel from "@/components/group-filter-panel/index.vue";
import { copyText } from "@/utils/clipboard";
import { defaultPagination } from "@/views/data/shared/metadata-utils";
import { factorTypeLabel, memberStatusTag } from "@/views/factor/shared/status";
import {
  buildOutputFieldCategoryItems,
  expandFactorOutputFields,
  filterFactorOutputFields,
  outputFieldsQueryFromRoute,
  outputFieldsQueryToRoute,
  type OutputFieldsFilter
} from "./output-fields-model";

defineOptions({ name: "FactorOutputFieldsTab" });

const props = defineProps<{
  spaceId: string;
  tasks: FactorTaskInfo[];
  tasksReady: boolean;
  categories: FactorCategory[];
  loading: boolean;
  loadError: string;
  taskLabelOf: (task: FactorTask) => string;
}>();

const emit = defineEmits<{
  openDetail: [factorId: string];
  openTask: [taskId: string];
  retryTasks: [];
}>();

const route = useRoute();
const router = useRouter();
const mobileCategoryVisible = ref(false);
const pagination = reactive(defaultPagination());

const filter = reactive<OutputFieldsFilter>(outputFieldsQueryFromRoute(route.query));
const searchKeyword = ref(filter.keyword);
let searchTimer: ReturnType<typeof setTimeout> | undefined;
let isActive = true;
let pendingQuerySync = 0;

const taskOptions = computed(() =>
  props.tasks.map(info => ({
    task_id: info.factor_task.task_id,
    label: props.taskLabelOf(info.factor_task)
  }))
);

const allRows = computed(() => expandFactorOutputFields(props.tasks, props.categories, props.taskLabelOf));
const filteredWithoutCategory = computed(() => filterFactorOutputFields(allRows.value, { ...filter, category: "" }));
const categoryItems = computed(() =>
  buildOutputFieldCategoryItems(props.categories, allRows.value, filteredWithoutCategory.value)
);
const filteredRows = computed(() => filterFactorOutputFields(allRows.value, filter));

const hasActiveFilter = computed(() =>
  Boolean(filter.category || filter.taskId || filter.kind || filter.status || filter.keyword)
);
const emptyText = computed(() => {
  if (props.loadError) return "计算任务加载失败，请重试";
  if (hasActiveFilter.value) return "没有符合条件的产出字段";
  return "当前空间尚无因子计算产出字段";
});

function isSameFilter(a: OutputFieldsFilter, b: OutputFieldsFilter) {
  return (
    a.category === b.category && a.taskId === b.taskId && a.kind === b.kind && a.status === b.status && a.keyword === b.keyword
  );
}

function clearSearchTimer() {
  if (!searchTimer) return;
  clearTimeout(searchTimer);
  searchTimer = undefined;
}

function applyQueryFromRoute(query: Record<string, unknown>) {
  const next = outputFieldsQueryFromRoute(query);
  if (isSameFilter(filter, next)) return;
  clearSearchTimer();
  Object.assign(filter, next);
  searchKeyword.value = next.keyword;
  pagination.current = 1;
}

function syncQueryToRoute() {
  if (!isActive || route.query.tab !== "fields") return;
  const nextQuery: Record<string, string> = { tab: "fields", ...outputFieldsQueryToRoute(filter) };
  if (typeof route.query.detail === "string" && route.query.detail) nextQuery.detail = route.query.detail;
  pendingQuerySync += 1;
  void Promise.resolve(router.replace({ path: route.path, query: nextQuery })).finally(() => {
    pendingQuerySync = Math.max(0, pendingQuerySync - 1);
  });
}

function reconcileFilters() {
  if (!isActive || !props.tasksReady || props.loading || props.loadError) return;
  let changed = false;
  if (filter.category && !categoryItems.value.some(item => item.key === filter.category)) {
    filter.category = "";
    changed = true;
  }
  if (filter.taskId && !taskOptions.value.some(item => item.task_id === filter.taskId)) {
    filter.taskId = "";
    changed = true;
  }
  if (changed) {
    pagination.current = 1;
    syncQueryToRoute();
  }
}

function scheduleSearch() {
  clearSearchTimer();
  searchTimer = setTimeout(commitSearch, 200);
}

function commitSearch() {
  clearSearchTimer();
  filter.keyword = searchKeyword.value.trim();
  pagination.current = 1;
  syncQueryToRoute();
}

function selectCategory(categoryId: string) {
  if (filter.category === categoryId) return;
  filter.category = categoryId;
  pagination.current = 1;
  syncQueryToRoute();
}

function selectCategoryFromMobile(categoryId: string) {
  mobileCategoryVisible.value = false;
  selectCategory(categoryId);
}

function onTaskChange(value: unknown) {
  filter.taskId = typeof value === "string" ? value : "";
  pagination.current = 1;
  syncQueryToRoute();
}

function onKindChange(value: unknown) {
  filter.kind = value === "fixed" || value === "var" ? (value as MemberKind) : "";
  pagination.current = 1;
  syncQueryToRoute();
}

function onStatusChange(value: unknown) {
  filter.status = value === "enabled" || value === "disabled" ? (value as MemberStatus) : "";
  pagination.current = 1;
  syncQueryToRoute();
}

function onPageSizeChange(size: number) {
  pagination.pageSize = size;
  pagination.current = 1;
}

function goTasks() {
  void router.push("/factor/tasks");
}

watch(
  () => filteredRows.value.length,
  total => {
    pagination.total = total;
    const maxPage = Math.max(1, Math.ceil(total / pagination.pageSize));
    if (pagination.current > maxPage) pagination.current = maxPage;
  },
  { immediate: true }
);

watch(
  [() => props.tasksReady, () => props.loading, () => props.loadError, categoryItems, taskOptions],
  () => {
    reconcileFilters();
  },
  { immediate: true }
);

watch(
  () => route.query,
  query => {
    if (!isActive || pendingQuerySync > 0 || query.tab !== "fields") return;
    applyQueryFromRoute(query);
    reconcileFilters();
  }
);

watch(
  () => props.spaceId,
  () => {
    clearSearchTimer();
    mobileCategoryVisible.value = false;
    // keep-alive 失活期间切空间：不抹除激活时由 URL 带入的筛选（例如从详情抽屉点「产出字段」跳入）。
    if (!isActive) return;
    Object.assign(filter, outputFieldsQueryFromRoute({}));
    searchKeyword.value = "";
    pagination.current = 1;
  },
  { flush: "sync" }
);

onActivated(() => {
  isActive = true;
  applyQueryFromRoute(route.query);
  reconcileFilters();
});

onDeactivated(() => {
  isActive = false;
  clearSearchTimer();
  mobileCategoryVisible.value = false;
});

onBeforeUnmount(() => {
  clearSearchTimer();
});
</script>

<style scoped>
.output-fields-tab {
  display: flex;
  min-height: calc(100vh - 180px);
  flex-direction: column;
}

.toolbar {
  display: flex;
  min-height: 32px;
  margin-bottom: var(--moox-space-2);
  align-items: center;
  justify-content: space-between;
  gap: var(--moox-space-3);
}

.toolbar-main {
  display: flex;
  min-width: 0;
  flex: 1 1 auto;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--moox-space-2);
}

.toolbar-meta {
  flex: 0 0 auto;
  white-space: nowrap;
}

.field-total {
  color: var(--color-text-3);
  font-size: 13px;
}

.keyword-control {
  min-width: 220px;
  flex: 1 1 300px;
  max-width: 400px;
}

.filter-control {
  width: 124px;
  flex: 0 0 124px;
}

.filter-control--task {
  width: 180px;
  flex: 0 0 180px;
}

.keyword-input,
.filter-select {
  width: 100%;
}

.mobile-category-trigger {
  display: none;
}

.output-fields-alert,
.output-fields-error {
  margin-bottom: var(--moox-space-2);
}

.output-fields-workbench {
  display: grid;
  min-height: 540px;
  flex: 1;
  grid-template-columns: 240px minmax(0, 1fr);
  border: 1px solid var(--color-border-2);
  background: var(--color-bg-2);
}

.category-panel {
  min-height: 0;
  overflow: auto;
  border-right: 1px solid var(--color-border-2);
  background: var(--color-fill-1);
}

.output-fields-main {
  min-width: 0;
  overflow: hidden;
}

.column-name-cell {
  display: flex;
  min-width: 0;
  align-items: center;
  justify-content: space-between;
  gap: var(--moox-space-1);
}

.column-name {
  display: flex;
  min-width: 0;
  flex: 1 1 auto;
  align-items: baseline;
  gap: var(--moox-space-1);
  white-space: nowrap;
}

.column-name code {
  overflow: hidden;
  color: var(--color-text-1);
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 13px;
  font-weight: 600;
  text-overflow: ellipsis;
}

.output-sub {
  flex: 0 0 auto;
  color: var(--color-text-3);
  font-size: 12px;
}

.copy-btn {
  flex: 0 0 auto;
  opacity: 0;
  transition: opacity 0.14s ease;
}

:deep(.arco-table-tr:hover) .copy-btn,
.copy-btn:focus-visible {
  opacity: 1;
}

.factor-cell,
.task-cell {
  display: flex;
  min-width: 0;
  flex-direction: column;
  line-height: 1.35;
}

.params-cell {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: var(--moox-space-1);
}

.params-text {
  min-width: 0;
  overflow: hidden;
  color: var(--color-text-2);
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.muted {
  color: var(--color-text-3);
  font-size: 12px;
}

.mono {
  overflow: hidden;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  text-overflow: ellipsis;
  white-space: nowrap;
}

@media (max-width: 900px) {
  .category-panel {
    display: none;
  }

  .output-fields-workbench {
    grid-template-columns: minmax(0, 1fr);
  }

  .mobile-category-trigger {
    display: inline-flex;
  }

  .toolbar {
    flex-wrap: wrap;
    gap: var(--moox-space-2);
  }

  .keyword-control {
    max-width: none;
    flex: 1 1 240px;
  }

  .filter-control,
  .filter-control--task {
    width: auto;
    flex: 1 1 130px;
  }
}
</style>
