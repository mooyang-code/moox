<template>
  <div class="definitions-tab">
    <div class="toolbar">
      <div class="toolbar-main">
        <a-button class="mobile-category-trigger" @click="mobileTreeVisible = true"
          ><template #icon><icon-menu /></template>因子分类</a-button
        >
        <div class="keyword-control">
          <a-input-search
            v-model="searchKeyword"
            class="keyword-input"
            allow-clear
            placeholder="搜索因子 ID 或名称"
            @input="scheduleSearch"
            @search="commitSearch"
            @clear="commitSearch"
          />
        </div>
        <div class="filter-control">
          <a-select v-model="filters.type" class="filter-select" allow-clear placeholder="因子类型" @change="clearSelection">
            <a-option value="timeseries">时序</a-option>
            <a-option value="cross_section">横截面</a-option>
          </a-select>
        </div>
        <div class="filter-control">
          <a-select v-model="usageFilter" class="filter-select" allow-clear placeholder="使用情况" @change="clearSelection">
            <a-option value="using">使用中（{{ counts.using }}）</a-option>
            <a-option value="idle">未使用（{{ counts.idle }}）</a-option>
          </a-select>
        </div>
        <a-button type="primary" :disabled="!spaceId" @click="commitSearch">
          <template #icon><icon-search /></template>查询
        </a-button>
      </div>
      <div class="toolbar-actions">
        <a-button type="primary" status="success" :disabled="!spaceId" @click="createFactor">
          <template #icon><icon-plus /></template>新建因子
        </a-button>
      </div>
    </div>

    <a-alert class="definitions-policy-alert" type="info" show-icon>
      因子定义只描述算法；要让因子运行，请到「计算任务」里添加并启用。
    </a-alert>

    <section class="definitions-workbench">
      <aside class="category-panel">
        <FactorCategoryTree
          :categories="treeCategories"
          :counts="treeCounts"
          :total="filteredWithoutCategory.length"
          :selected="selectedCategory"
          :saving="categorySaving"
          @select="selectCategory"
          @create="createCategory"
          @rename="renameCategory"
          @delete="confirmDeleteCategory"
        />
      </aside>

      <main class="definitions-main">
        <DefinitionBatchBar
          :selected-count="selectedKeys.length"
          :categories="treeCategories"
          :loading="batchLoading"
          @move="moveSelected"
          @clear="selectedKeys = []"
        />
        <a-alert v-if="loadError" type="error" class="table-error"
          >因子定义加载失败：{{ loadError }} <a-link @click="load">重试</a-link></a-alert
        >
        <DefinitionTable
          :rows="rows"
          :loading="loading"
          :pagination="pagination"
          :selected-keys="selectedKeys"
          :empty-text="emptyText"
          @select="selectedKeys = $event"
          @detail="emit('openDetail', $event)"
          @edit="editFactor"
          @remove="removeFactor"
          @open-task="emit('openTask', $event)"
          @page-change="pagination.current = $event"
          @page-size-change="changePageSize"
        />
      </main>
    </section>

    <a-drawer v-model:visible="mobileTreeVisible" title="因子分类" width="280px" :footer="false">
      <FactorCategoryTree
        :categories="treeCategories"
        :counts="treeCounts"
        :total="filteredWithoutCategory.length"
        :selected="selectedCategory"
        :saving="categorySaving"
        @select="selectCategory"
        @create="createCategory"
        @rename="renameCategory"
        @delete="confirmDeleteCategory"
      />
    </a-drawer>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { Message, Modal } from "@arco-design/web-vue";
import { deleteFactor, deleteFactorCategory, listFactors, setFactorCategory, upsertFactorCategory } from "@/api/factor";
import { readAllPages } from "@/api/read-all-pages";
import type { FactorCategory, FactorInfo, FactorTask, FactorTaskInfo } from "@/api/factor/types";
import { createLatestRequestGuard } from "@/utils/latest-request";
import DefinitionBatchBar from "./definition-batch-bar.vue";
import DefinitionTable from "./definition-table.vue";
import {
  categoryCounts,
  categoryOf,
  definitionRows,
  deleteCategoryMovingFactors,
  filterDefinitions,
  moveFactorsToCategory,
  orderedCategories,
  usageCounts,
  type DefinitionFilters,
  type UsageFilter
} from "./definitions-model";
import FactorCategoryTree from "./factor-category-tree.vue";

defineOptions({ name: "FactorDefinitionsTab" });

const props = defineProps<{
  spaceId: string;
  categories: FactorCategory[];
  tasks: FactorTaskInfo[];
  taskLabelOf: (task: FactorTask) => string;
}>();

const emit = defineEmits<{
  categoriesChanged: [];
  tasksChanged: [];
  openDetail: [factorId: string];
  openTask: [taskId: string];
}>();

const PAGE_SIZE = 500;

const route = useRoute();
const router = useRouter();

const filters = reactive<DefinitionFilters>({ type: "", usage: "all", keyword: "" });
const searchKeyword = ref("");
const categorySaving = ref(false);
const batchLoading = ref(false);
const mobileTreeVisible = ref(false);
const items = ref<FactorInfo[]>([]);
const loading = ref(false);
const loadError = ref("");
const selectedKeys = ref<string[]>([]);
const pagination = reactive({ current: 1, pageSize: 20, pageSizeOptions: [20, 50, 100], showPageSize: true, showTotal: true });
const gate = createLatestRequestGuard();
let searchTimer: ReturnType<typeof setTimeout> | undefined;

const usageFilter = computed<UsageFilter | undefined>({
  get: () => (filters.usage === "all" ? undefined : filters.usage),
  set: value => {
    filters.usage = value || "all";
  }
});

const selectedCategory = computed(() => (typeof route.query.category === "string" ? route.query.category : ""));
const counts = computed(() => usageCounts(items.value));
/** 先按关键词、类型、使用情况过滤，分类树据此计数；再按选中的分类过滤出表格。 */
const filteredWithoutCategory = computed(() => filterDefinitions(items.value, { ...filters, category: "" }));
const treeCounts = computed(() => categoryCounts(filteredWithoutCategory.value));
const treeCategories = computed(() => orderedCategories(props.categories));
const emptyText = computed(() =>
  filters.keyword || filters.type || filters.usage !== "all" ? "没有符合条件的因子定义" : "当前范围暂无因子定义"
);

function labelOfTask(taskId: string) {
  const info = props.tasks.find((item: FactorTaskInfo) => item.factor_task.task_id === taskId);
  return info ? props.taskLabelOf(info.factor_task) : taskId;
}

const rows = computed(() =>
  definitionRows(
    filteredWithoutCategory.value.filter(item => !selectedCategory.value || categoryOf(item.factor) === selectedCategory.value),
    props.categories,
    labelOfTask
  )
);

function clearSelection() {
  selectedKeys.value = [];
  pagination.current = 1;
}

function scheduleSearch() {
  if (searchTimer) clearTimeout(searchTimer);
  searchTimer = setTimeout(commitSearch, 300);
}

function commitSearch() {
  if (searchTimer) clearTimeout(searchTimer);
  filters.keyword = searchKeyword.value.trim();
  clearSelection();
}

function changePageSize(size: number) {
  pagination.pageSize = size;
  pagination.current = 1;
}

/** 选中的分类写进 ?category=；query 变化时页面不重建，关键词等筛选保持。 */
function selectCategory(categoryId: string) {
  mobileTreeVisible.value = false;
  if (categoryId === selectedCategory.value) return;
  clearSelection();
  const query = { ...route.query };
  if (categoryId) query.category = categoryId;
  else delete query.category;
  void router.replace({ path: route.path, query });
}

async function createCategory(category: { category_id: string; name: string }, done: () => void) {
  if (props.categories.some(item => item.category_id === category.category_id)) {
    Message.warning("分类 ID 已存在");
    return;
  }
  const sortOrder = Math.max(0, ...props.categories.map(item => item.sort_order ?? 0)) + 1;
  await saveCategory({ ...category, sort_order: sortOrder }, "分类已创建", done);
}

/** 重命名时带上原 sort_order：Upsert 会覆盖排序。 */
async function renameCategory(category: FactorCategory, name: string, done: () => void) {
  await saveCategory({ category_id: category.category_id, name, sort_order: category.sort_order }, "分类已重命名", done);
}

async function saveCategory(
  category: Pick<FactorCategory, "category_id" | "name" | "sort_order">,
  success: string,
  done: () => void
) {
  categorySaving.value = true;
  try {
    await upsertFactorCategory(category);
    Message.success(success);
    done();
    emit("categoriesChanged");
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "保存分类失败");
  } finally {
    categorySaving.value = false;
  }
}

function confirmDeleteCategory(category: FactorCategory) {
  const moving = items.value.filter(item => categoryOf(item.factor) === category.category_id).length;
  Modal.confirm({
    title: "删除分类",
    content: moving ? `「${category.name}」下的 ${moving} 个因子将归入未分类，确认删除？` : `确认删除分类「${category.name}」？`,
    okText: "删除",
    okButtonProps: { status: "danger" },
    onOk: () => deleteCategory(category)
  });
}

async function deleteCategory(category: FactorCategory) {
  try {
    const result = await deleteCategoryMovingFactors(category.category_id, items.value, {
      setCategory: setFactorCategory,
      deleteCategory: deleteFactorCategory
    });
    if (result.deleted) {
      Message.success("分类已删除");
      if (selectedCategory.value === category.category_id) selectCategory("");
    } else {
      const reason = result.error instanceof Error ? result.error.message : "未知错误";
      Message.error(`因子 ${result.failed} 归入未分类失败（${reason}），分类未删除`);
    }
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "删除分类失败");
  } finally {
    emit("categoriesChanged");
    emit("tasksChanged");
    await load();
  }
}

async function moveSelected(categoryId: string) {
  batchLoading.value = true;
  let movedCount = 0;
  try {
    const result = await moveFactorsToCategory([...selectedKeys.value], categoryId, setFactorCategory);
    movedCount = result.moved.length;
    if (result.failed) {
      const reason = result.error instanceof Error ? result.error.message : "未知错误";
      Message.error(`已移动 ${result.moved.length} 个，因子 ${result.failed} 移动失败（${reason}）`);
    } else {
      Message.success(`已移动 ${result.moved.length} 个因子`);
      selectedKeys.value = [];
    }
  } finally {
    batchLoading.value = false;
    if (movedCount > 0) emit("tasksChanged");
    await load();
  }
}

/** 列表默认不带源码（D24）；详情抽屉再按需 GetFactor。 */
async function load() {
  const token = gate.begin();
  loading.value = true;
  try {
    const all = await readAllPages(async page => {
      const rsp = await listFactors({ page: { page, size: PAGE_SIZE } });
      return { items: rsp.factors || [], hasMore: Boolean(rsp.page_result?.has_more) };
    });
    if (!token.isLatest()) return;
    items.value = all;
    loadError.value = "";
    const present = new Set(all.map(item => item.factor.factor_id));
    selectedKeys.value = selectedKeys.value.filter(id => present.has(id));
  } catch (error) {
    if (token.isLatest()) loadError.value = error instanceof Error ? error.message : "因子定义加载失败";
  } finally {
    if (token.isLatest()) loading.value = false;
  }
}

function createFactor() {
  void router.push("/factor/definitions/new");
}

function editFactor(factorId: string) {
  void router.push(`/factor/definitions/${encodeURIComponent(factorId)}/edit`);
}

async function removeFactor(factorId: string) {
  try {
    await deleteFactor(factorId);
    Message.success("因子已删除");
    emit("tasksChanged");
    await load();
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "删除因子失败");
  }
}

watch(
  () => rows.value.length,
  total => {
    const maxPage = Math.max(1, Math.ceil(total / pagination.pageSize));
    if (pagination.current > maxPage) pagination.current = maxPage;
  }
);

watch(
  () => props.spaceId,
  next => {
    selectedKeys.value = [];
    pagination.current = 1;
    items.value = [];
    loadError.value = "";
    if (next) void load();
  },
  { immediate: true }
);

onBeforeUnmount(() => {
  if (searchTimer) clearTimeout(searchTimer);
  gate.invalidate();
});
</script>

<style scoped>
.definitions-tab {
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
  gap: var(--moox-space-4);
}

.toolbar-main {
  display: flex;
  min-width: 0;
  flex: 1 1 auto;
  align-items: center;
  gap: var(--moox-space-2);
}

.toolbar-actions {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
  gap: var(--moox-space-2);
  white-space: nowrap;
}

.keyword-control {
  min-width: 220px;
  flex: 1 1 360px;
}

.filter-control {
  width: 140px;
  flex: 0 0 140px;
}

.keyword-input,
.filter-select {
  width: 100%;
}

.mobile-category-trigger {
  display: none;
}

.definitions-policy-alert {
  margin-bottom: var(--moox-space-2);
}

.definitions-workbench {
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

.definitions-main {
  min-width: 0;
  overflow: hidden;
}

.table-error {
  margin: var(--moox-space-2);
}

@media (max-width: 900px) {
  .category-panel {
    display: none;
  }

  .definitions-workbench {
    grid-template-columns: minmax(0, 1fr);
  }

  .mobile-category-trigger {
    display: inline-flex;
  }

  .toolbar {
    flex-wrap: wrap;
    gap: var(--moox-space-2);
  }

  .toolbar-main {
    flex: 1 1 100%;
    flex-wrap: wrap;
  }

  .toolbar-actions {
    margin-left: auto;
  }

  .keyword-control {
    min-width: 220px;
    flex: 1 1 260px;
  }

  .filter-control {
    width: auto;
    flex: 1 1 130px;
  }
}

@media (max-width: 560px) {
  .toolbar-main,
  .toolbar-actions {
    width: 100%;
  }

  .toolbar-actions {
    justify-content: flex-end;
  }

  .definitions-workbench {
    min-height: 480px;
  }
}
</style>
