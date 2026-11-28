<template>
  <div class="fields-tab">
    <div class="toolbar">
      <div class="toolbar-main">
        <a-button class="mobile-group-trigger" @click="mobileGroupVisible = true">
          <template #icon><icon-menu /></template>
          字段组
        </a-button>
        <div class="keyword-control">
          <a-input-search
            v-model="searchKeyword"
            class="keyword-input"
            allow-clear
            placeholder="搜索字段 ID、中文名、提供方或数据集"
            @input="scheduleSearch"
            @search="commitSearch"
            @clear="commitSearch"
          />
        </div>
        <div class="filter-control">
          <a-select
            :model-value="filter.category || undefined"
            class="filter-select"
            allow-clear
            placeholder="类别"
            @change="onCategoryChange"
          >
            <a-option value="base">基础字段</a-option>
            <a-option value="extra">附加字段</a-option>
            <a-option value="dataset">数据集列</a-option>
          </a-select>
        </div>
        <div class="filter-control">
          <a-select
            :model-value="filter.valueType || undefined"
            class="filter-select"
            allow-clear
            placeholder="值类型"
            @change="onValueTypeChange"
          >
            <a-option v-for="item in canonicalFieldValueTypeOptions" :key="item.value" :value="item.value">
              {{ item.label }}
            </a-option>
          </a-select>
        </div>
        <div class="filter-control">
          <a-select
            :model-value="filter.landed || undefined"
            class="filter-select"
            allow-clear
            placeholder="落地状态"
            @change="onLandedChange"
          >
            <a-option value="landed">已落地数据集</a-option>
            <a-option value="spec_only">仅规格可选</a-option>
          </a-select>
        </div>
        <a-tag v-if="filter.provider" closable color="arcoblue" @close="clearProvider"> 来源：{{ activeProviderLabel }} </a-tag>
      </div>
      <div class="toolbar-meta">
        <span class="field-total">共 {{ filteredRows.length }} 个字段</span>
      </div>
    </div>

    <a-alert class="field-policy-alert" type="info" show-icon>
      展示当前空间的全部原始数据字段（K 线基础、附加字段与非因子数据集列，只读）；因子计算产出列请到「因子计算 › 因子定义」查看。
    </a-alert>

    <a-alert v-if="loadWarning" class="field-warning-alert" type="warning" show-icon>
      {{ loadWarning }} <a-link @click="load">重试</a-link>
    </a-alert>

    <section class="field-workbench">
      <aside class="group-panel">
        <GroupFilterPanel
          title="字段组"
          all-label="全部字段"
          :items="groupItems"
          :total="filteredWithoutGroup.length"
          :selected="filter.group"
          :loading="loading"
          empty-text="暂无字段组"
          @select="selectGroup"
        />
      </aside>

      <main class="field-main">
        <FieldCatalogTable
          :rows="filteredRows"
          :loading="loading"
          :pagination="pagination"
          :empty-text="emptyText"
          @detail="detailField = $event"
          @page-change="pagination.current = $event"
          @page-size-change="onPageSizeChange"
        />
      </main>
    </section>

    <a-drawer v-model:visible="mobileGroupVisible" title="字段组" width="280px" :footer="false">
      <GroupFilterPanel
        title="字段组"
        all-label="全部字段"
        :items="groupItems"
        :total="filteredWithoutGroup.length"
        :selected="filter.group"
        :loading="loading"
        empty-text="暂无字段组"
        @select="selectGroupFromMobile"
      />
    </a-drawer>

    <FieldDetailDrawer :field="detailField" @close="detailField = null" />
  </div>
</template>

<script setup lang="ts">
import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, reactive, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { GetDataTypeConfigs } from "@/api/collector";
import { listAllDatasetColumns, listAllDatasets } from "@/api/storage/metadata";
import GroupFilterPanel from "@/components/group-filter-panel/index.vue";
import { createLatestRequestGuard } from "@/utils/latest-request";
import { canonicalFieldValueTypeOptions, defaultPagination, normalizeFieldValueType } from "@/views/data/shared/metadata-utils";
import { isFactorResultDataset } from "@/views/data/shared/module-attribution";
import FieldCatalogTable from "./components/FieldCatalogTable.vue";
import FieldDetailDrawer from "./components/FieldDetailDrawer.vue";
import {
  buildFieldGroupItems,
  fieldTabQueryFromRoute,
  fieldTabQueryToRoute,
  filterFieldCatalog,
  mapWithConcurrency,
  mergeFieldCatalog,
  type CatalogFieldItem,
  type DatasetColumnBundle,
  type FieldCatalogCategory,
  type FieldCatalogFilter,
  type FieldLandedFilter
} from "./field-catalog";

defineOptions({ name: "DataFieldsTab" });

const props = defineProps<{ spaceId: string }>();

const route = useRoute();
const router = useRouter();
const requests = createLatestRequestGuard();

const allItems = ref<CatalogFieldItem[]>([]);
const loading = ref(false);
const loadWarning = ref("");
const mobileGroupVisible = ref(false);
const detailField = ref<CatalogFieldItem | null>(null);
const pagination = reactive(defaultPagination());

const filter = reactive<FieldCatalogFilter>(fieldTabQueryFromRoute(route.query));
const searchKeyword = ref(filter.keyword);
let searchTimer: ReturnType<typeof setTimeout> | undefined;
let isActive = true;
let requestedSpaceId = "";
let pendingQuerySync = 0;

const filteredWithoutGroup = computed(() => filterFieldCatalog(allItems.value, { ...filter, group: "" }));
const groupItems = computed(() => buildFieldGroupItems(allItems.value, filteredWithoutGroup.value));
const filteredRows = computed(() => filterFieldCatalog(allItems.value, filter));
const activeProviderLabel = computed(() => {
  const target = filter.provider.trim().toLowerCase();
  if (!target) return "";
  for (const item of allItems.value) {
    const matched = item.extra_scopes.find(scope => scope.provider_id.toLowerCase() === target);
    if (matched?.provider_name) return matched.provider_name;
  }
  return filter.provider;
});

const hasActiveFilter = computed(() =>
  Boolean(filter.group || filter.keyword || filter.category || filter.valueType || filter.landed || filter.provider)
);
const emptyText = computed(() => (hasActiveFilter.value ? "没有符合条件的字段" : "当前空间暂无字段"));

function isSameFilter(a: FieldCatalogFilter, b: FieldCatalogFilter) {
  return (
    a.group === b.group &&
    a.keyword === b.keyword &&
    a.category === b.category &&
    a.valueType === b.valueType &&
    a.landed === b.landed &&
    a.provider === b.provider
  );
}

function clearSearchTimer() {
  if (!searchTimer) return;
  clearTimeout(searchTimer);
  searchTimer = undefined;
}

function applyQueryFromRoute(query: Record<string, unknown>) {
  const next = fieldTabQueryFromRoute(query);
  if (isSameFilter(filter, next)) return;
  clearSearchTimer();
  Object.assign(filter, next);
  searchKeyword.value = next.keyword;
  pagination.current = 1;
}

function syncQueryToRoute() {
  if (!isActive || route.query.tab !== "fields") return;
  const nextQuery: Record<string, string> = { tab: "fields", ...fieldTabQueryToRoute(filter) };
  pendingQuerySync += 1;
  void Promise.resolve(router.replace({ path: route.path, query: nextQuery })).finally(() => {
    pendingQuerySync = Math.max(0, pendingQuerySync - 1);
  });
}

function reconcileGroupFilter() {
  if (!isActive || loading.value || loadWarning.value || !filter.group) return;
  if (!groupItems.value.some(item => item.key === filter.group)) {
    filter.group = "";
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

function selectGroup(group: string) {
  if (filter.group === group) return;
  filter.group = group;
  pagination.current = 1;
  syncQueryToRoute();
}

function selectGroupFromMobile(group: string) {
  mobileGroupVisible.value = false;
  selectGroup(group);
}

function onCategoryChange(value: unknown) {
  filter.category = value === "base" || value === "extra" || value === "dataset" ? (value as FieldCatalogCategory) : "";
  pagination.current = 1;
  syncQueryToRoute();
}

function onValueTypeChange(value: unknown) {
  filter.valueType = normalizeFieldValueType(typeof value === "string" ? value : "");
  pagination.current = 1;
  syncQueryToRoute();
}

function onLandedChange(value: unknown) {
  filter.landed = value === "landed" || value === "spec_only" ? (value as FieldLandedFilter) : "";
  pagination.current = 1;
  syncQueryToRoute();
}

function clearProvider() {
  filter.provider = "";
  pagination.current = 1;
  syncQueryToRoute();
}

function onPageSizeChange(size: number) {
  pagination.pageSize = size;
  pagination.current = 1;
}

async function load() {
  const spaceId = props.spaceId.trim();
  const request = requests.begin();
  detailField.value = null;
  if (spaceId !== requestedSpaceId) {
    allItems.value = [];
  }
  if (!spaceId) {
    requestedSpaceId = "";
    allItems.value = [];
    loadWarning.value = "";
    loading.value = false;
    return;
  }
  requestedSpaceId = spaceId;
  loading.value = true;
  loadWarning.value = "";

  try {
    const [specResult, datasetsResult] = await Promise.allSettled([GetDataTypeConfigs(), listAllDatasets(spaceId)]);
    if (!request.isLatest()) return;

    const specFailed = specResult.status === "rejected";
    const datasetsFailed = datasetsResult.status === "rejected";
    const detail =
      specResult.status === "fulfilled" ? specResult.value.configs?.find(item => item.data_type === "kline") : undefined;

    const bundles: DatasetColumnBundle[] = [];
    let failedColumns = 0;
    if (datasetsResult.status === "fulfilled") {
      const nonFactorDatasets = datasetsResult.value.filter(dataset => !isFactorResultDataset(dataset));
      const columnResults = await mapWithConcurrency(nonFactorDatasets, 6, async dataset => ({
        dataset,
        columns: await listAllDatasetColumns(spaceId, dataset.dataset_id)
      }));
      if (!request.isLatest()) return;

      for (const item of columnResults) {
        if (item.status === "fulfilled") bundles.push(item.value);
        else failedColumns += 1;
      }
    }

    allItems.value = mergeFieldCatalog({
      spaceId,
      baseFields: detail?.base_output_fields,
      extraFields: detail?.extra_fields,
      datasetBundles: bundles
    });
    pagination.total = filteredRows.value.length;

    const warnings: string[] = [];
    if (specFailed && datasetsFailed) {
      warnings.push("采集字段规格与当前空间数据集列均加载失败");
    } else {
      if (specFailed) warnings.push("采集字段规格加载失败，仅展示数据集列");
      if (datasetsFailed) warnings.push("当前空间数据集列加载失败，仅展示采集规格字段");
    }
    if (failedColumns > 0) {
      warnings.push(`有 ${failedColumns} 个数据集的列清单加载失败，已展示其余字段`);
    }
    loadWarning.value = warnings.join("；");
    if (specFailed && datasetsFailed) {
      requestedSpaceId = "";
    }
  } catch {
    if (!request.isLatest()) return;
    requestedSpaceId = "";
    allItems.value = [];
    loadWarning.value = "字段目录加载异常";
  } finally {
    if (request.isLatest()) {
      loading.value = false;
      if (!loadWarning.value) reconcileGroupFilter();
    }
  }
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
  () => route.query,
  query => {
    if (!isActive || pendingQuerySync > 0 || query.tab !== "fields") return;
    applyQueryFromRoute(query);
    reconcileGroupFilter();
  }
);

onMounted(() => {
  isActive = true;
  if (props.spaceId.trim()) void load();
});

onActivated(() => {
  isActive = true;
  applyQueryFromRoute(route.query);
  const spaceId = props.spaceId.trim();
  if (spaceId && requestedSpaceId !== spaceId) {
    void load();
  } else {
    reconcileGroupFilter();
  }
});

onDeactivated(() => {
  isActive = false;
  clearSearchTimer();
  detailField.value = null;
  mobileGroupVisible.value = false;
});

onBeforeUnmount(() => {
  clearSearchTimer();
  requests.invalidate();
});
</script>

<style scoped>
.fields-tab {
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
  max-width: 420px;
}

.filter-control {
  width: 132px;
  flex: 0 0 132px;
}

.keyword-input,
.filter-select {
  width: 100%;
}

.mobile-group-trigger {
  display: none;
}

.field-policy-alert,
.field-warning-alert {
  margin-bottom: var(--moox-space-2);
}

.field-workbench {
  display: grid;
  min-height: 540px;
  flex: 1;
  grid-template-columns: 240px minmax(0, 1fr);
  border: 1px solid var(--color-border-2);
  background: var(--color-bg-2);
}

.group-panel {
  min-height: 0;
  overflow: auto;
  border-right: 1px solid var(--color-border-2);
  background: var(--color-fill-1);
}

.field-main {
  min-width: 0;
  overflow: hidden;
}

@media (max-width: 900px) {
  .group-panel {
    display: none;
  }

  .field-workbench {
    grid-template-columns: minmax(0, 1fr);
  }

  .mobile-group-trigger {
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

  .filter-control {
    width: auto;
    flex: 1 1 124px;
  }
}
</style>
