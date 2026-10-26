<template>
  <div class="moox-page definitions-page">
    <div class="moox-inner definitions-inner">
      <div class="toolbar">
        <div class="toolbar-main">
          <h2 class="page-title">因子定义</h2>
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
            <template #icon><icon-plus /></template>新增因子
          </a-button>
        </div>
      </div>

      <a-alert class="definitions-policy-alert" type="info" show-icon>
        因子定义只描述算法；要让因子运行，请到「计算任务」里添加并启用。
      </a-alert>
      <a-alert v-if="!spaceId" type="warning" show-icon>请先在顶部选择空间</a-alert>
      <template v-else>
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
              @detail="detailId = $event"
              @edit="editFactor"
              @remove="removeFactor"
              @open-task="openTask"
              @page-change="pagination.current = $event"
              @page-size-change="changePageSize"
            />
          </main>
        </section>
      </template>

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
      <FactorDetailDrawer :factor-id="detailId" :categories="categories" @close="detailId = ''" @open-task="openTask" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onActivated, reactive, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { Message, Modal } from "@arco-design/web-vue";
import {
  deleteFactor,
  deleteFactorCategory,
  listFactorCategories,
  listFactors,
  setFactorCategory,
  upsertFactorCategory
} from "@/api/factor";
import type { FactorCategory, FactorInfo, FactorTaskInfo } from "@/api/factor/types";
import { RequestGate } from "@/utils/request-gate";
import { useFactorScope } from "@/views/factor/shared/use-factor-scope";
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
import DefinitionBatchBar from "./definition-batch-bar.vue";
import DefinitionTable from "./definition-table.vue";
import FactorCategoryTree from "./factor-category-tree.vue";
import FactorDetailDrawer from "./factor-detail-drawer.vue";

defineOptions({ name: "FactorDefinitions" });

const PAGE_SIZE = 500;

const route = useRoute();
const router = useRouter();
const { store, spaceId } = useFactorScope({ poll: false });

// 与字段页一致：关键词输入后 300ms 生效（回车或点查询立即生效），下拉筛选改动即生效；分类由左侧树决定。
const filters = reactive<DefinitionFilters>({ type: "", usage: "all", keyword: "" });
const searchKeyword = ref("");
const categories = ref<FactorCategory[]>([]);
const categorySaving = ref(false);
const batchLoading = ref(false);
const mobileTreeVisible = ref(false);
const items = ref<FactorInfo[]>([]);
const loading = ref(false);
const loadError = ref("");
const detailId = ref("");
const selectedKeys = ref<string[]>([]);
const pagination = reactive({ current: 1, pageSize: 20, pageSizeOptions: [20, 50, 100], showPageSize: true, showTotal: true });
const gate = new RequestGate();
let searchTimer: ReturnType<typeof setTimeout> | undefined;

/** 使用情况下拉清空时回到“全部”。 */
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
const treeCategories = computed(() => orderedCategories(categories.value));
const emptyText = computed(() =>
  filters.keyword || filters.type || filters.usage !== "all" ? "没有符合条件的因子定义" : "当前范围暂无因子定义"
);

function setLabelOf(taskId: string) {
  const info = store.sets.find((item: FactorTaskInfo) => item.factor_task.task_id === taskId);
  return info ? store.setLabel(info.factor_task) : taskId;
}

const rows = computed(() =>
  definitionRows(
    filteredWithoutCategory.value.filter(item => !selectedCategory.value || categoryOf(item.factor) === selectedCategory.value),
    categories.value,
    setLabelOf
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

/** 选中的分类写进 ?category=；路由声明了 reuseOnQuery，页面不重建，关键词等筛选保持。 */
function selectCategory(categoryId: string) {
  mobileTreeVisible.value = false;
  if (categoryId === selectedCategory.value) return;
  clearSelection();
  const query = { ...route.query };
  if (categoryId) query.category = categoryId;
  else delete query.category;
  void router.replace({ path: route.path, query });
}

/** 分类只用于归类与筛选；加载失败时分类显示为 ID，不影响列表。 */
async function loadCategories() {
  try {
    categories.value = await listFactorCategories();
  } catch {
    categories.value = [];
  }
}

async function createCategory(category: { category_id: string; name: string }, done: () => void) {
  if (categories.value.some(item => item.category_id === category.category_id)) {
    Message.warning("分类 ID 已存在");
    return;
  }
  const sortOrder = Math.max(0, ...categories.value.map(item => item.sort_order ?? 0)) + 1;
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
    await loadCategories();
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
    await Promise.all([load(), loadCategories()]);
  }
}

async function moveSelected(categoryId: string) {
  batchLoading.value = true;
  try {
    const result = await moveFactorsToCategory([...selectedKeys.value], categoryId, setFactorCategory);
    if (result.failed) {
      const reason = result.error instanceof Error ? result.error.message : "未知错误";
      Message.error(`已移动 ${result.moved.length} 个，因子 ${result.failed} 移动失败（${reason}）`);
    } else {
      Message.success(`已移动 ${result.moved.length} 个因子`);
      selectedKeys.value = [];
    }
  } finally {
    batchLoading.value = false;
    await load();
  }
}

/** 列表默认不带源码（D24）；详情抽屉再按需 GetFactor。 */
async function load() {
  const token = gate.next();
  loading.value = true;
  try {
    const all: FactorInfo[] = [];
    for (let page = 1; ; page += 1) {
      const rsp = await listFactors({ page: { page, size: PAGE_SIZE } });
      const batch = rsp.factors || [];
      all.push(...batch);
      if (!rsp.page_result?.has_more || !batch.length) break;
    }
    if (!gate.isCurrent(token)) return;
    items.value = all;
    loadError.value = "";
    const present = new Set(all.map(item => item.factor.factor_id));
    selectedKeys.value = selectedKeys.value.filter(id => present.has(id));
  } catch (error) {
    if (gate.isCurrent(token)) loadError.value = error instanceof Error ? error.message : "因子定义加载失败";
  } finally {
    if (gate.isCurrent(token)) loading.value = false;
  }
}

function createFactor() {
  void router.push("/factor/definitions/new");
}

function editFactor(factorId: string) {
  void router.push(`/factor/definitions/${encodeURIComponent(factorId)}/edit`);
}

function openTask(taskId: string) {
  void router.push({ path: "/factor/tasks", query: { detail: taskId } });
}

async function removeFactor(factorId: string) {
  try {
    await deleteFactor(factorId);
    Message.success("因子已删除");
    await load();
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "删除因子失败");
  }
}

// 成员在计算任务里被添加 / 移除 / 启停后，使用情况会变化；切回本页（keep-alive 激活）时重新拉取。
let fresh = true;
// 请求按空间授权：空间初始化完成（或切换）后才加载，避免无空间时被 403 且不再恢复。
watch(
  spaceId,
  next => {
    selectedKeys.value = [];
    if (next) {
      void load();
      void loadCategories();
    }
  },
  { immediate: true }
);
onActivated(() => {
  if (fresh) {
    fresh = false;
    return;
  }
  if (!spaceId.value) return;
  void load();
  void store.reload();
});
</script>

<style scoped>
.definitions-inner {
  display: flex;
  min-height: calc(100vh - 116px);
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
.page-title {
  flex: 0 0 auto;
  margin: 0 var(--moox-space-2) 0 0;
  font-size: 20px;
  font-weight: 600;
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
  min-height: 560px;
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
