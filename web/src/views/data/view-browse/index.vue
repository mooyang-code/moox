<template>
  <div class="moox-page view-browse-page">
    <div class="moox-inner">
      <a-alert v-if="!selectedSpaceId" type="warning" show-icon>请先在顶部选择空间</a-alert>

      <a-spin v-else :loading="metaLoading">
        <a-alert v-if="metaError" type="error" show-icon class="query-alert">
          {{ metaError }}
          <a-button type="text" size="mini" :loading="metaLoading" @click="loadMeta">重试</a-button>
        </a-alert>
        <a-empty v-if="datasets.length === 0" :description="props.emptyDescription" />

        <template v-else>
          <section v-if="datasets.length > 1" class="view-tabs-row">
            <a-tabs
              v-model:active-key="activeDatasetKey"
              type="rounded"
              size="medium"
              class="view-tabs"
              @change="onDatasetChange"
            >
              <a-tab-pane v-for="dataset in datasets" :key="dataset.dataset_id" :title="datasetDisplayName(dataset)" />
            </a-tabs>
          </section>

          <section class="view-status-line">
            <span>数据集: {{ currentDatasetName }} ({{ activeDataset?.dataset_id || "-" }})</span>
            <span v-if="activeDataset?.frequency">频率: {{ activeDataset.frequency }}</span>
            <span v-if="currentDatasetRetention">保留期: {{ currentDatasetRetention }}</span>
            <a-tag size="small" :color="mode === 'time_series' ? 'blue' : 'green'">{{ modeText }}</a-tag>
            <a-tag size="small" :color="viewIndexTag.color">{{ viewIndexTag.text }}</a-tag>
            <span v-if="viewIndexColumnsText">{{ viewIndexColumnsText }}</span>
            <span v-if="viewIndexCoverageText">覆盖: {{ viewIndexCoverageText }}</span>
            <slot name="status-extra" />
          </section>

          <a-alert v-if="queryError" class="query-alert" :type="viewIndexPreparing ? 'warning' : 'error'" show-icon>
            {{ queryError }}
            <span v-if="rowsStale">；数据状态待刷新，保留上次查询数据（{{ lastRowsReadAt }}）</span>
          </a-alert>
          <a-alert v-else-if="hasQueried && !loading && tableRows.length === 0" class="query-alert" type="info" show-icon>
            {{ hasAnyFilter ? "没有符合条件的行" : props.emptyRowsDescription }}
          </a-alert>

          <div class="view-query-controls">
            <QueryControls
              v-if="activeDataset"
              v-model:filters="filters"
              v-model:record-keyword="recordKeyword"
              :mode="mode"
              :field-options="filterFieldOptions"
              :column-title="columnTitle"
              :loading="loading"
              :kline-available="klineAvailable"
              :kline-loading="klineLoading"
              :preview-range-text="previewRangeText"
              :zone="props.zone"
              @query="applyQueryControls"
              @record-search="onRecordSearch"
              @reset="resetQueryControls"
              @kline="openKlineModal"
            >
              <template v-if="$slots['toolbar-extra']" #toolbar-extra><slot name="toolbar-extra" /></template>
              <template v-if="$slots['query-extra']" #query-extra><slot name="query-extra" /></template>
            </QueryControls>
          </div>

          <ResultTable
            :mode="mode"
            :rows="tableRows"
            :column-names="visibleColumnNames"
            :all-column-names="tableColumnNames"
            :restrict-detail-columns="Boolean(props.displayColumns)"
            :column-title="columnTitle"
            :system-columns="props.displayColumns?.system"
            :sort-field="sortState.fieldName"
            :sort-direction="sortState.direction"
            :loading="loading || contextLoading"
            :has-queried="hasQueried"
            :has-more="previewHasMore"
            :page="pagination.current"
            :page-size="DEFAULT_VIEW_PAGE_SIZE"
            :kline-available="klineAvailable"
            :row-extra-title="props.rowExtraTitle"
            :zone="props.zone"
            @sort="setSort"
            @prev="onPreviewPrevPage"
            @next="onPreviewNextPage"
            @row-kline="openRowKline"
          >
            <template v-if="$slots['row-extra']" #row-extra="slotProps"><slot name="row-extra" v-bind="slotProps" /></template>
          </ResultTable>
        </template>
      </a-spin>
    </div>

    <KlineModal
      v-model:visible="klineVisible"
      v-model:limit="klineLimit"
      :subject-id="klineSubjectId"
      :freq="klineFreq"
      :records="klineRecords"
      :loading="klineLoading"
      :zone="props.zone"
      @reload="reloadKlineRecords"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { listAllDatasetColumns } from "@/api/storage/metadata";
import {
  getViewState,
  isViewNotFoundError,
  queryTimeSeriesRows,
  searchRecordRows,
  type GetViewStateRsp
} from "@/api/storage/view";
import type { Dataset, DatasetColumn, FieldValueType } from "@/api/storage/types";
import { useSpaceStore } from "@/store/modules/space";
import { formatRetention, retentionSourceLabel } from "@/utils/retention";
import {
  buildColumnLabels,
  buildViewFilterExprs,
  buildViewFilterFieldOptions,
  buildViewSorts,
  datasetDisplayName,
  defaultSeriesTag,
  recordRowsToTableRows,
  rowsToColumnNames,
  timeSeriesRowsToTableRows,
  viewModeFromDataset,
  type ViewFilterFieldOption,
  type ViewFilterState,
  type ViewSortDirection
} from "./view-browse-utils";
import KlineModal from "./kline-modal.vue";
import QueryControls from "./query-controls.vue";
import ResultTable from "./result-table.vue";
import { loadTargetedDatasets } from "./composables/view-browse-catalog";
import { useViewKline, type ViewBrowseTableRow } from "./composables/use-view-kline";
import { formatInstant } from "@/utils/time-zone";
import { usePolling } from "@/hooks/usePolling";

defineOptions({ name: "DataViewBrowse" });

const props = withDefaults(
  defineProps<{
    emptyDescription?: string;
    emptyRowsDescription?: string;
    autoRefreshIntervalMs?: number;
    /** 要浏览的数据集（每个数据集恰好有一个查询索引）；多个时按页签切换。切空间后由调用方换成新空间的数据集或重新挂载本组件。 */
    datasetIds?: string[];
    activeDatasetId?: string;
    refreshKey?: number;
    /** 为 false 时只加载元数据、不发查询：调用方先准备好投影与附加条件，再放行首查。 */
    queryReady?: boolean;
    /** 表格显示的系统键（数据ID、时间始终显示）与业务列及其顺序；不传时显示全部列。 */
    displayColumns?: { system: string[]; business: string[] };
    /** 时序查询的 column_names，只含业务列且不能为空；不传时投影数据集全部启用的列。 */
    projectColumns?: string[];
    /** 逐列搜索只为这些业务列生成（系统键始终保留）；不传时为全部列。 */
    searchColumns?: string[];
    /** 追加到查询的条件（须自带 valueType），与搜索栅格的条件按“且”组合；不作用于 K 线。 */
    extraFilters?: ViewFilterState[];
    /** row-extra 插槽所在列的标题。 */
    rowExtraTitle?: string;
    /** 显示和输入的时区（由调用方按空间给）：时间列、TIME 字段、TIME 筛选、K 线图都按它。 */
    zone?: string;
  }>(),
  {
    zone: "UTC",
    emptyDescription: "暂无数据集",
    emptyRowsDescription: "当前数据集查询成功，但结果为空。",
    autoRefreshIntervalMs: 0,
    datasetIds: undefined,
    activeDatasetId: "",
    refreshKey: 0,
    queryReady: true,
    displayColumns: undefined,
    projectColumns: undefined,
    searchColumns: undefined,
    extraFilters: undefined,
    rowExtraTitle: ""
  }
);

const emit = defineEmits<{ (event: "cleared"): void }>();

const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);

const datasets = ref<Dataset[]>([]);
const targetedDatasetIds = computed(() => [...new Set((props.datasetIds || []).map(id => id.trim()).filter(Boolean))]);
/** 数据集启用的列；停用的列读取时隐藏。 */
const columns = ref<DatasetColumn[]>([]);
let metaLoadSequence = 0;
let viewContextSequence = 0;
let viewRowsRequestSequence = 0;
const activeDatasetKey = ref("");
const tableRows = ref<ViewBrowseTableRow[]>([]);
const tableColumnNames = ref<string[]>([]);
const visibleColumnNames = computed(() => props.displayColumns?.business ?? tableColumnNames.value);

const recordKeyword = ref("");
const filters = ref<ViewFilterState[]>([]);
// 时序预览先回答“最新一根是哪根”：不显式排序时 DuckDB 按索引顺序返回，
// 采集正常时首页也可能停在一根旧 K 线上。
const sortState = reactive<{ fieldName: string; direction: ViewSortDirection }>({ fieldName: "data_time", direction: "desc" });
const metaLoading = ref(false);
const metaError = ref("");
const contextLoading = ref(false);
const loading = ref(false);
const pendingRowsRefresh = ref(false);
const rowsStale = ref(false);
const lastRowsReadAt = ref("");
const queryError = ref("");
const queryErrorIsPreparing = ref(false);
const viewIndexPreparing = computed(() => queryErrorIsPreparing.value);
function isViewIndexNotReadyError(value: string) {
  return /view index .* is not prepared: view index not ready/i.test(value);
}

function viewQueryErrorMessage(error: unknown, fallback: string) {
  const message = error instanceof Error ? error.message : fallback;
  queryErrorIsPreparing.value = isViewIndexNotReadyError(message);
  return queryErrorIsPreparing.value ? "查询索引创建中，就绪后请刷新查看。" : message;
}

/** 查询索引状态：读不到时按“未知”显示，不影响查询。 */
const viewIndex = ref<{ status: "unknown" | "ready" | "building" | "unregistered"; state?: GetViewStateRsp }>({
  status: "unknown"
});
const viewIndexTag = computed(() => {
  switch (viewIndex.value.status) {
    case "ready":
      return { color: "green", text: "已构建" };
    case "building":
      return { color: "orange", text: "构建中" };
    case "unregistered":
      return { color: "orange", text: "未构建" };
    default:
      return { color: "gray", text: "索引状态未知" };
  }
});
const viewIndexColumnsText = computed(() => {
  const built = viewIndex.value.status === "ready" ? viewIndex.value.state?.columns?.length : undefined;
  if (built === undefined) return "";
  const declared = columns.value.length;
  return declared && declared !== built ? `已建列: ${built}/${declared}` : `已建列: ${built}`;
});
const viewIndexCoverageText = computed(() => {
  const state = viewIndex.value.status === "ready" ? viewIndex.value.state : undefined;
  if (!state?.indexed_from && !state?.indexed_to) return "";
  return `${formatInstant(state.indexed_from, props.zone)} ~ ${formatInstant(state.indexed_to, props.zone)}`;
});

const hasQueried = ref(false);
const previewHasMore = ref(false);
const VIEW_BROWSE_UNSCOPED_PREVIEW_WINDOW_HOURS = 24;
const VIEW_BROWSE_SCOPED_PREVIEW_WINDOW_DAYS = 7;
const DEFAULT_VIEW_PAGE_SIZE = 25;
const KLINE_COLUMN_BASENAMES = ["open_time", "open", "high", "low", "close", "volume"];

const pagination = reactive({
  current: 1
});

const activeDataset = computed(() => datasets.value.find(item => item.dataset_id === activeDatasetKey.value));
const currentDatasetName = computed(() => datasetDisplayName(activeDataset.value) || "-");
const currentDatasetRetention = computed(() => {
  const dataset = activeDataset.value;
  if (!dataset?.retention) return "";
  return `${formatRetention(dataset.retention)}（${retentionSourceLabel(dataset.retention_source)}）`;
});

const mode = computed(() => viewModeFromDataset(activeDataset.value));
const modeText = computed(() => {
  if (mode.value === "time_series") return "时序数据 / DuckDB";
  if (mode.value === "record") return "记录数据 / Bleve";
  return "未知类型";
});

const columnNames = computed(() => columns.value.map(item => item.column_name).filter(Boolean));
const klineColumnNames = computed(() => KLINE_COLUMN_BASENAMES.filter(name => columnNames.value.includes(name)));
// 有开高低收四列才能画 K 线（因子结果等数据集没有）：没有时不显示 K 线入口
const klineAvailable = computed(() => ["open", "high", "low", "close"].every(name => klineColumnNames.value.includes(name)));
const columnLabels = computed(() => buildColumnLabels(columns.value));
const filterFieldOptions = computed(() =>
  buildViewFilterFieldOptions(mode.value, columns.value, columnLabels.value, props.searchColumns)
);

const {
  klineVisible,
  klineSubjectId,
  klineFreq,
  klineRecords,
  klineLoading,
  klineLimit,
  openRowKline,
  openKlineModal,
  reloadKlineRecords,
  closeKlineModal,
  invalidateKline
} = useViewKline({
  spaceId: () => spaceStore.requireSpaceId(),
  datasetId: () => activeDataset.value?.dataset_id,
  zone: () => props.zone,
  filters,
  columnNames: () => klineColumnNames.value,
  ownFilterExprs: () => activeFilterExprs(false),
  previewRange: () => defaultTimeSeriesPreviewRange()
});

async function loadMeta() {
  const sequence = ++metaLoadSequence;
  const space_id = selectedSpaceId.value;
  if (!space_id) {
    metaLoading.value = false;
    metaError.value = "";
    return;
  }
  metaLoading.value = true;
  metaError.value = "";
  try {
    const items = await loadTargetedDatasets(space_id, targetedDatasetIds.value);
    if (sequence !== metaLoadSequence) return;
    datasets.value = items;
    ensureSelectedDataset();
    await loadViewContext();
  } catch (error) {
    if (sequence === metaLoadSequence) {
      metaError.value = error instanceof Error ? error.message : "加载数据集失败";
      Message.error(metaError.value);
    }
  } finally {
    if (sequence === metaLoadSequence) metaLoading.value = false;
  }
}

function ensureSelectedDataset() {
  if (!datasets.value.length) {
    activeDatasetKey.value = "";
    return;
  }
  const requested = props.activeDatasetId || activeDatasetKey.value;
  if (requested && datasets.value.some(item => item.dataset_id === requested)) {
    activeDatasetKey.value = requested;
  } else if (!activeDatasetKey.value || !datasets.value.some(item => item.dataset_id === activeDatasetKey.value)) {
    activeDatasetKey.value = datasets.value[0].dataset_id;
  }
}

watch(
  () => props.activeDatasetId,
  () => {
    const current = activeDatasetKey.value;
    ensureSelectedDataset();
    if (activeDatasetKey.value !== current) {
      clearViewState();
      void loadViewContext();
    }
  }
);

watch(
  () => targetedDatasetIds.value.join("\u0000"),
  (next, previous) => {
    if (next === previous) return;
    datasets.value = [];
    activeDatasetKey.value = "";
    clearViewState();
    void loadMeta();
  }
);

watch(
  () => props.refreshKey,
  () => {
    void refreshRowsInBackground();
  }
);

// 查询或上下文加载期间的刷新合并成之后的一次补读，不在忙时悄悄丢掉用户的刷新。
watch([pendingRowsRefresh, loading, contextLoading, metaLoading, () => activeDataset.value?.dataset_id], () => {
  if (!pendingRowsRefresh.value || !activeDataset.value || loading.value || contextLoading.value || metaLoading.value) return;
  pendingRowsRefresh.value = false;
  void reloadRows(true);
});

async function onDatasetChange() {
  clearViewState();
  await loadViewContext();
}

function clearViewState() {
  viewContextSequence += 1;
  viewRowsRequestSequence += 1;
  contextLoading.value = false;
  loading.value = false;
  columns.value = [];
  tableRows.value = [];
  tableColumnNames.value = [];
  filters.value = [];
  resetSortState();
  closeKlineModal();
  queryError.value = "";
  rowsStale.value = false;
  lastRowsReadAt.value = "";
  hasQueried.value = false;
  previewHasMore.value = false;
  pagination.current = 1;
  viewIndex.value = { status: "unknown" };
}

/** 读取当前数据集查询索引的状态；尽力而为，失败只把状态记为未知。 */
async function refreshViewIndex() {
  const space_id = selectedSpaceId.value;
  const dataset_id = activeDataset.value?.dataset_id;
  if (!space_id || !dataset_id) return;
  const isCurrent = () => activeDataset.value?.dataset_id === dataset_id;
  try {
    const state = await getViewState({ space_id, dataset_id });
    if (isCurrent()) viewIndex.value = { status: state.index_id ? "ready" : "building", state };
  } catch (error) {
    if (!isCurrent()) return;
    viewIndex.value = { status: isViewNotFoundError(error) ? "unregistered" : "unknown" };
    if (!isViewNotFoundError(error)) console.warn("读取查询索引状态失败", error);
  }
}

async function loadViewContext() {
  const requestSequence = ++viewContextSequence;
  const space_id = selectedSpaceId.value;
  const dataset = activeDataset.value;
  if (!space_id || !dataset) {
    contextLoading.value = false;
    return;
  }

  contextLoading.value = true;
  try {
    const datasetId = dataset.dataset_id;
    void refreshViewIndex();
    const loaded = await listAllDatasetColumns(space_id, datasetId);
    if (requestSequence !== viewContextSequence || activeDataset.value?.dataset_id !== datasetId) {
      return;
    }
    columns.value = loaded.filter(column => column.status === "active");
    resetFilterRows();
    resetSortState();
    await reloadRows();
  } catch (error) {
    if (requestSequence === viewContextSequence) {
      Message.error(error instanceof Error ? error.message : "加载数据集列失败");
    }
  } finally {
    if (requestSequence === viewContextSequence) contextLoading.value = false;
  }
}

/**
 * background：自动刷新。不弹提示、开头不清错误条（成功后才清），失败时保留上次的行。
 * 手动操作（含父页的“刷新”）失败时页面自己弹一次提示；请求层一律静默，免得同一个错误弹两次。
 */
interface ReloadOptions {
  background?: boolean;
}

async function reloadRows(retainRowsOnError = false, options: ReloadOptions = {}) {
  if (!props.queryReady) return;
  if (!retainRowsOnError || !lastRowsReadAt.value) tableColumnNames.value = columnNames.value;
  if (!activeDataset.value) return;
  if (mode.value === "time_series") {
    await loadTimeSeriesRows(retainRowsOnError, options);
    return;
  }
  if (mode.value === "record") {
    await loadRecordRows(retainRowsOnError, options);
  }
}

async function refreshRowsInBackground() {
  pendingRowsRefresh.value = true;
  void refreshViewIndex();
  if (!activeDataset.value && !metaLoading.value) await loadMeta();
}

/** 自动刷新的一轮：正在读取时跳过，不排队；数据集元数据没读到时不自动重试（会弹提示），等用户点“刷新”。 */
async function pollRows() {
  if (loading.value || contextLoading.value || metaLoading.value || !activeDataset.value) return;
  void refreshViewIndex();
  await reloadRows(true, { background: true });
}

/** 结果属于当前请求与数据集：切换数据集或卸载后迟到的响应丢弃。 */
function rowsRequestIsCurrent(requestSequence: number, _space_id: string, dataset_id: string) {
  return requestSequence === viewRowsRequestSequence && activeDataset.value?.dataset_id === dataset_id;
}

function applyRows(rows: ViewBrowseTableRow[], rawRows: Parameters<typeof rowsToColumnNames>[0], hasMore: boolean) {
  tableRows.value = rows;
  tableColumnNames.value = rowsToColumnNames(rawRows, columnNames.value);
  previewHasMore.value = hasMore;
  hasQueried.value = true;
  rowsStale.value = false;
  queryError.value = "";
  lastRowsReadAt.value = formatInstant(Date.now(), props.zone, "clock");
}

function applyRowsError(error: unknown, fallback: string, retainRowsOnError: boolean, background: boolean) {
  queryError.value = viewQueryErrorMessage(error, fallback);
  rowsStale.value = retainRowsOnError && !!lastRowsReadAt.value;
  if (!rowsStale.value) {
    tableRows.value = [];
    previewHasMore.value = false;
    lastRowsReadAt.value = "";
  }
  hasQueried.value = true;
  if (!viewIndexPreparing.value && !background) Message.error(queryError.value);
}

async function loadTimeSeriesRows(retainRowsOnError = false, { background = false }: ReloadOptions = {}) {
  const space_id = spaceStore.requireSpaceId();
  const dataset_id = activeDataset.value?.dataset_id;
  if (!dataset_id) return;
  const requestSequence = ++viewRowsRequestSequence;
  loading.value = true;
  if (!background) queryError.value = "";
  try {
    const timeRange = defaultTimeSeriesPreviewRange();
    const projection = props.projectColumns ?? columnNames.value;
    const rsp = await queryTimeSeriesRows({
      space_id,
      dataset_id,
      ...(timeRange ? { time_range: timeRange } : {}),
      ...(projection.length > 0 ? { column_names: projection } : {}),
      filter: activeFilterExprs(),
      sorts: buildViewSorts(sortState),
      page: { page: pagination.current, size: DEFAULT_VIEW_PAGE_SIZE },
      total_mode: "NONE"
    });
    if (!rowsRequestIsCurrent(requestSequence, space_id, dataset_id)) return;
    const rows = rsp.rows || [];
    const tableItems = timeSeriesRowsToTableRows(rows, props.zone).map((row, index) => ({
      ...row,
      freq: rows[index]?.key?.time_series?.frequency || "-",
      seriesTag: rows[index]?.key?.time_series?.series_tag || "",
      rawKey: rows[index]?.key
    }));
    applyRows(tableItems, rows, !!rsp.page_result?.has_more);
  } catch (error) {
    if (!rowsRequestIsCurrent(requestSequence, space_id, dataset_id)) return;
    applyRowsError(error, "查询时序数据失败", retainRowsOnError, background);
  } finally {
    if (requestSequence === viewRowsRequestSequence) loading.value = false;
  }
}

async function loadRecordRows(retainRowsOnError = false, { background = false }: ReloadOptions = {}) {
  const space_id = spaceStore.requireSpaceId();
  const dataset_id = activeDataset.value?.dataset_id;
  if (!dataset_id) return;
  const requestSequence = ++viewRowsRequestSequence;
  loading.value = true;
  if (!background) queryError.value = "";
  try {
    const rsp = await searchRecordRows({
      space_id,
      dataset_id,
      text_query: recordKeyword.value.trim(),
      filter: activeFilterExprs(),
      sorts: buildViewSorts(sortState),
      page: { page: pagination.current, size: DEFAULT_VIEW_PAGE_SIZE }
    });
    if (!rowsRequestIsCurrent(requestSequence, space_id, dataset_id)) return;
    const rows = rsp.rows || [];
    applyRows(recordRowsToTableRows(rows, props.zone), rows, !!rsp.page_result?.has_more);
  } catch (error) {
    if (!rowsRequestIsCurrent(requestSequence, space_id, dataset_id)) return;
    applyRowsError(error, "查询记录数据失败", retainRowsOnError, background);
  } finally {
    if (requestSequence === viewRowsRequestSequence) loading.value = false;
  }
}

async function onRecordSearch() {
  pagination.current = 1;
  await reloadRows();
}

async function applyQueryControls() {
  pagination.current = 1;
  await reloadRows();
}

async function resetQueryControls() {
  recordKeyword.value = "";
  resetSortState();
  resetFilterRows();
  pagination.current = 1;
  // 先让调用方清掉附加条件；条件确有变化时由下面的 watch 重查，避免同一次清空查两遍。
  const before = extraFiltersKey.value;
  emit("cleared");
  await nextTick();
  if (extraFiltersKey.value !== before) return;
  await reloadRows();
}

function resetSortState() {
  // 结果是时序数据：最新周期排在最前，免得首页停在旧周期上，看起来像因子停算了。
  if (mode.value === "time_series") {
    sortState.fieldName = "data_time";
    sortState.direction = "desc";
    return;
  }
  sortState.fieldName = "";
  sortState.direction = "";
}

function resetFilterRows() {
  filters.value = filterFieldOptions.value.map(option => createFilterState(option));
}

// 搜索列随调用方的列设置变化时，保留仍在的检索框里已输入的内容。
watch(
  () => filterFieldOptions.value.map(option => option.value).join("\u0000"),
  () => {
    if (!columns.value.length) return;
    const existing = new Map(filters.value.map(filter => [filter.fieldName, filter]));
    filters.value = filterFieldOptions.value.map(option => existing.get(option.value) ?? createFilterState(option));
  }
);

function createFilterState(option?: ViewFilterFieldOption): ViewFilterState {
  return {
    fieldName: option?.value || "",
    operator: option?.value === "series_tag" ? "eq" : "contains",
    valueType: option?.valueType || "FIELD_VALUE_TYPE_STRING",
    value: option?.value === "series_tag" ? defaultSeriesTag(tableRows.value) : "",
    startValue: "",
    endValue: ""
  };
}

function activeFilterExprs(includeExtra = true) {
  const own: ViewFilterState[] = filters.value.map(filter => ({
    ...filter,
    valueType: filterValueType(filter.fieldName)
  }));
  return buildViewFilterExprs(includeExtra ? own.concat(props.extraFilters ?? []) : own, props.zone);
}

const extraFiltersKey = computed(() => JSON.stringify(props.extraFilters ?? []));
const projectionKey = computed(() => JSON.stringify([props.projectColumns ?? null, props.displayColumns?.business ?? null]));

// 调用方放行首查；放行前已经加载好的上下文在这里补查第一页。
watch(
  () => props.queryReady,
  ready => {
    if (!ready || !activeDataset.value || contextLoading.value) return;
    pagination.current = 1;
    void reloadRows();
  }
);

// 附加条件变化回到第一页；投影变化重查当前页。首查之前的变化由首查本身带上。
watch(extraFiltersKey, () => {
  if (!hasQueried.value || !activeDataset.value || contextLoading.value) return;
  pagination.current = 1;
  void reloadRows();
});
watch(projectionKey, () => {
  if (!hasQueried.value || !activeDataset.value || contextLoading.value) return;
  void reloadRows();
});

function defaultTimeSeriesPreviewRange() {
  if (hasActiveDataTimeFilter()) return undefined;
  const end = new Date(Date.now() + 5 * 60 * 1000);
  const windowHours = hasExactSubjectIDFilter()
    ? VIEW_BROWSE_SCOPED_PREVIEW_WINDOW_DAYS * 24
    : VIEW_BROWSE_UNSCOPED_PREVIEW_WINDOW_HOURS;
  const start = new Date(end.getTime() - windowHours * 60 * 60 * 1000);
  return {
    start_time: start.toISOString(),
    end_time: end.toISOString()
  };
}

function hasExactSubjectIDFilter() {
  return filters.value.some(
    filter => filter.fieldName.trim() === "subject_id" && filter.operator === "eq" && !!(filter.value || "").trim()
  );
}

/** 有任何筛选条件（含父页传入的）：结果为空时说“没有符合条件的行”，而不是“结果为空”。 */
const hasAnyFilter = computed(() => Boolean(activeFilterExprs()) || Boolean(recordKeyword.value.trim()));

/** 时序数据没有按数据时间筛选时，只查最近一段时间：写在查询区，免得以为“查不到”就是没有数据。 */
const previewRangeText = computed(() => {
  if (mode.value !== "time_series" || hasActiveDataTimeFilter()) return "";
  return hasExactSubjectIDFilter()
    ? `没有指定数据时间，只查最近 ${VIEW_BROWSE_SCOPED_PREVIEW_WINDOW_DAYS} 天`
    : `未指定标的时只查最近 ${VIEW_BROWSE_UNSCOPED_PREVIEW_WINDOW_HOURS} 小时；指定标的（subject_id 等于）后查最近 ${VIEW_BROWSE_SCOPED_PREVIEW_WINDOW_DAYS} 天`;
});

function hasActiveDataTimeFilter() {
  return filters.value.some(filter => filter.fieldName.trim() === "data_time" && hasFilterInput(filter));
}

function hasFilterInput(filter: ViewFilterState) {
  if (filter.operator === "empty" || filter.operator === "not_empty") {
    return true;
  }
  if (filter.operator === "range") {
    return !!(filter.startValue || "").trim() || !!(filter.endValue || "").trim();
  }
  return !!(filter.value || "").trim();
}

function filterValueType(fieldName: string): FieldValueType {
  return filterFieldOptions.value.find(item => item.value === fieldName)?.valueType || "FIELD_VALUE_TYPE_STRING";
}

async function onPreviewPrevPage() {
  if (pagination.current <= 1 || loading.value || contextLoading.value) return;
  pagination.current -= 1;
  await reloadRows();
}

async function onPreviewNextPage() {
  if (!previewHasMore.value || loading.value || contextLoading.value) return;
  pagination.current += 1;
  await reloadRows();
}

function columnTitle(columnName: string) {
  return columnLabels.value[columnName] || columnName;
}

async function setSort(fieldName: string, direction: ViewSortDirection) {
  if (sortState.fieldName === fieldName && sortState.direction === direction) {
    sortState.fieldName = "";
    sortState.direction = "";
  } else {
    sortState.fieldName = fieldName;
    sortState.direction = direction;
  }
  pagination.current = 1;
  await reloadRows();
}

onMounted(loadMeta);
// 自动刷新（调用方给了间隔时）：页面隐藏时暂停、不叠加；后台读取失败不弹提示，保留上次的数据并标出“待刷新”
usePolling(pollRows, props.autoRefreshIntervalMs || 60_000, () => (props.autoRefreshIntervalMs || 0) > 0);
onBeforeUnmount(() => {
  metaLoadSequence += 1;
  viewContextSequence += 1;
  viewRowsRequestSequence += 1;
  invalidateKline();
});
</script>

<style scoped>
.view-query-controls {
  width: 100%;
  max-width: 100%;
  min-width: 0;
}

.view-browse-page {
  width: 100%;
  max-width: 100%;
  height: 100%;
  min-width: 0;
  overflow-x: hidden;
}

.view-browse-page :deep(.arco-spin),
.view-browse-page :deep(.arco-spin-children) {
  display: block;
  width: 100%;
  max-width: 100%;
  min-width: 0;
}

.view-tabs-row {
  min-width: 0;
  margin-bottom: var(--moox-space-3);
}

.view-tabs :deep(.arco-tabs-content) {
  display: none;
}

.view-status-line {
  display: flex;
  align-items: center;
  gap: var(--moox-space-2);
  min-height: 34px;
  margin-bottom: var(--moox-space-3);
  color: var(--color-text-3);
}

.query-alert {
  margin-bottom: var(--moox-space-3);
}

@media (max-width: 560px) {
  .view-status-line {
    align-items: flex-start;
    flex-direction: column;
  }
}
</style>
