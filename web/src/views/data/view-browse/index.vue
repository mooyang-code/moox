<template>
  <div class="view-browse-page">
    <div class="page-head">
      <div>
        <h2>视图浏览</h2>
      </div>
      <a-space>
        <a-button :disabled="!selectedSpaceId" :loading="metaLoading || contextLoading" @click="loadMeta">
          <template #icon><icon-refresh /></template>
          刷新
        </a-button>
        <a-button :disabled="!activeView" :loading="loading" @click="reloadRows">
          <template #icon><icon-sync /></template>
          重新查询
        </a-button>
        <a-button :disabled="!activeView" :loading="rebuildLoading" @click="rebuildActiveView">
          <template #icon><icon-refresh /></template>
          重建视图
        </a-button>
      </a-space>
    </div>

    <a-alert v-if="!selectedSpaceId" type="warning" show-icon>请先在顶部选择空间</a-alert>

    <a-spin v-else :loading="metaLoading">
      <a-empty v-if="views.length === 0" description="暂无查询视图" />

      <template v-else>
        <section class="view-tabs-row">
          <a-tabs v-model:active-key="activeViewId" type="rounded" size="medium" class="view-tabs" @change="onViewChange">
            <a-tab-pane v-for="view in views" :key="view.view_id" :title="viewDisplayName(view)" />
          </a-tabs>
        </section>

        <section class="view-status-line">
          <span>{{ currentDatasetName }}</span>
          <a-tag size="small" :color="mode === 'time_series' ? 'blue' : 'green'">{{ modeText }}</a-tag>
          <a-tag size="small" :color="activeView?.active_result ? 'green' : 'orange'">
            {{ activeView?.active_result ? '已构建' : '未构建' }}
          </a-tag>
          <span v-if="activeView?.active_view_version">活跃版本 {{ activeView.active_view_version }}</span>
        </section>

        <a-alert v-if="queryError" class="query-alert" type="error" show-icon>{{ queryError }}</a-alert>
        <a-alert v-else-if="hasQueried && !loading && tableRows.length === 0" class="query-alert" type="info" show-icon>
          当前视图查询成功，但结果为空。
        </a-alert>

        <section v-if="activeView" class="view-query-panel">
          <div class="filter-grid">
            <div v-if="mode === 'record'" class="filter-item record-keyword-filter">
              <label>全文检索:</label>
              <div class="filter-control">
                <a-input
                  v-model="recordKeyword"
                  allow-clear
                  placeholder="关键词"
                  @press-enter="onRecordSearch"
                />
                <span class="operator-static">⌕</span>
              </div>
            </div>

            <div
              v-for="filter in filters"
              :key="filter.fieldName"
              class="filter-item"
              :class="{ 'range-filter-item': filter.operator === 'range' }"
            >
              <label :title="filterFieldLabel(filter.fieldName)">{{ filterFieldLabel(filter.fieldName) }}:</label>
              <div class="filter-control" :class="{ 'empty-filter-control': filter.operator === 'empty' || filter.operator === 'not_empty' }">
                <template v-if="filter.operator === 'range'">
                  <a-input v-model="filter.startValue" allow-clear placeholder="开始值" @press-enter="applyQueryControls" />
                  <span class="range-separator">-</span>
                  <a-input v-model="filter.endValue" allow-clear placeholder="结束值" @press-enter="applyQueryControls" />
                </template>
                <a-input
                  v-else-if="filter.operator !== 'empty' && filter.operator !== 'not_empty'"
                  v-model="filter.value"
                  allow-clear
                  placeholder="检索值"
                  @press-enter="applyQueryControls"
                />
                <span v-else class="empty-filter-placeholder">{{ filter.operator === 'empty' ? '为空' : '非空' }}</span>
                <a-dropdown trigger="click" @select="setFilterOperator(filter, $event)">
                  <button class="operator-button" type="button" :title="filterOperatorTitle(filter.operator)">
                    {{ filterOperatorSymbol(filter.operator) }}
                  </button>
                  <template #content>
                    <a-doption v-for="option in filterOperatorOptions" :key="option.value" :value="option.value">
                      {{ option.label }}
                    </a-doption>
                  </template>
                </a-dropdown>
              </div>
            </div>
          </div>

          <div class="query-actions">
            <a-button size="small" type="primary" :loading="loading" @click="applyQueryControls">查询</a-button>
            <a-button v-if="mode === 'time_series'" size="small" type="outline" :loading="klineLoading" @click="openKlineModal">
              <template #icon><icon-bar-chart /></template>
              K线
            </a-button>
            <a-button size="small" @click="resetQueryControls">清空</a-button>
          </div>
        </section>

        <section v-if="mode === 'time_series'" class="result-pane">
          <a-table
            row-key="id"
            size="small"
            :bordered="{ cell: true }"
            :loading="loading || contextLoading"
            :data="tableRows"
            :pagination="tablePagination"
            :scroll="{ x: 'max-content', y: 500 }"
            @page-change="onPageChange"
            @page-size-change="onPageSizeChange"
          >
            <template #columns>
              <a-table-column title="序号" :width="72" align="center" fixed="left">
                <template #cell="{ rowIndex }">{{ (pagination.current - 1) * pagination.pageSize + rowIndex + 1 }}</template>
              </a-table-column>
              <a-table-column data-index="key" :width="180" fixed="left">
                <template #title><span class="sortable-title">数据ID<span class="sort-arrows"><button :class="sortArrowClass('subject_id', 'asc')" @click.stop="setSort('subject_id', 'asc')">▲</button><button :class="sortArrowClass('subject_id', 'desc')" @click.stop="setSort('subject_id', 'desc')">▼</button></span></span></template>
              </a-table-column>
              <a-table-column data-index="freq" :width="96">
                <template #title><span class="sortable-title">频率<span class="sort-arrows"><button :class="sortArrowClass('freq', 'asc')" @click.stop="setSort('freq', 'asc')">▲</button><button :class="sortArrowClass('freq', 'desc')" @click.stop="setSort('freq', 'desc')">▼</button></span></span></template>
              </a-table-column>
              <a-table-column data-index="version" :width="230">
                <template #title><span class="sortable-title">时间<span class="sort-arrows"><button :class="sortArrowClass('data_time', 'asc')" @click.stop="setSort('data_time', 'asc')">▲</button><button :class="sortArrowClass('data_time', 'desc')" @click.stop="setSort('data_time', 'desc')">▼</button></span></span></template>
              </a-table-column>
              <a-table-column
                v-for="column in tableColumnNames"
                :key="column"
                :width="dynamicColumnWidth(column)"
                :ellipsis="true"
                :tooltip="true"
              >
                <template #title>
                  <span class="sortable-title">
                    {{ columnTitle(column) }}
                    <span class="sort-arrows">
                      <button :class="sortArrowClass(column, 'asc')" @click.stop="setSort(column, 'asc')">▲</button>
                      <button :class="sortArrowClass(column, 'desc')" @click.stop="setSort(column, 'desc')">▼</button>
                    </span>
                  </span>
                </template>
                <template #cell="{ record }">{{ record.values[column] || '-' }}</template>
              </a-table-column>
              <a-table-column title="操作" :width="92" align="center" fixed="right">
                <template #cell="{ record }">
                  <a-button type="text" size="mini" @click="openDetail(record)">查看</a-button>
                </template>
              </a-table-column>
            </template>
          </a-table>
        </section>

        <section v-else-if="mode === 'record'" class="result-pane">
          <a-table
            row-key="id"
            size="small"
            :bordered="{ cell: true }"
            :loading="loading || contextLoading"
            :data="tableRows"
            :pagination="tablePagination"
            :scroll="{ x: 'max-content', y: 460 }"
            @page-change="onPageChange"
            @page-size-change="onPageSizeChange"
          >
            <template #columns>
              <a-table-column title="序号" :width="72" align="center" fixed="left">
                <template #cell="{ rowIndex }">{{ (pagination.current - 1) * pagination.pageSize + rowIndex + 1 }}</template>
              </a-table-column>
              <a-table-column data-index="key" :width="200" fixed="left">
                <template #title><span class="sortable-title">记录ID<span class="sort-arrows"><button :class="sortArrowClass('record_id', 'asc')" @click.stop="setSort('record_id', 'asc')">▲</button><button :class="sortArrowClass('record_id', 'desc')" @click.stop="setSort('record_id', 'desc')">▼</button></span></span></template>
              </a-table-column>
              <a-table-column data-index="version" :width="230">
                <template #title><span class="sortable-title">版本<span class="sort-arrows"><button :class="sortArrowClass('version', 'asc')" @click.stop="setSort('version', 'asc')">▲</button><button :class="sortArrowClass('version', 'desc')" @click.stop="setSort('version', 'desc')">▼</button></span></span></template>
              </a-table-column>
              <a-table-column
                v-for="column in tableColumnNames"
                :key="column"
                :width="dynamicColumnWidth(column)"
                :ellipsis="true"
                :tooltip="true"
              >
                <template #title>
                  <span class="sortable-title">
                    {{ columnTitle(column) }}
                    <span class="sort-arrows">
                      <button :class="sortArrowClass(column, 'asc')" @click.stop="setSort(column, 'asc')">▲</button>
                      <button :class="sortArrowClass(column, 'desc')" @click.stop="setSort(column, 'desc')">▼</button>
                    </span>
                  </span>
                </template>
                <template #cell="{ record }">{{ record.values[column] || '-' }}</template>
              </a-table-column>
              <a-table-column title="操作" :width="92" align="center" fixed="right">
                <template #cell="{ record }">
                  <a-button type="text" size="mini" @click="openDetail(record)">查看</a-button>
                </template>
              </a-table-column>
            </template>
          </a-table>
        </section>

        <a-empty v-else description="无法识别该视图的主数据集类型" />
      </template>
    </a-spin>

    <a-modal v-model:visible="detailVisible" title="视图数据详情" width="820px" :footer="false">
      <div v-if="detailRow" class="detail-body">
        <a-descriptions :column="2" bordered>
          <a-descriptions-item :label="mode === 'time_series' ? '数据ID' : '记录ID'">{{ detailRow.key }}</a-descriptions-item>
          <a-descriptions-item :label="mode === 'time_series' ? '时间' : '版本'">{{ detailRow.version }}</a-descriptions-item>
        </a-descriptions>
        <a-table :data="detailColumns" :pagination="false" :bordered="{ cell: true }" size="small" class="detail-table">
          <template #columns>
            <a-table-column title="字段名" data-index="name" :width="220" />
            <a-table-column title="字段值" data-index="value" :ellipsis="true" :tooltip="true" />
          </template>
        </a-table>
      </div>
    </a-modal>

    <a-modal v-model:visible="klineVisible" :title="klineTitle" width="1080px" :footer="false">
      <a-spin class="kline-spin" :loading="klineLoading">
        <div class="kline-modal-body">
          <div class="kline-toolbar">
            <div class="kline-symbol-block">
              <strong>{{ klineSubjectId }}</strong>
              <span>{{ klineFreq }}</span>
              <span>{{ klineCandleCount }} 根</span>
            </div>
            <div class="kline-control-strip">
              <a-tooltip :content="klinePlaying ? '停止播放' : '播放K线'">
                <a-button
                  class="kline-play-button"
                  size="small"
                  type="outline"
                  shape="circle"
                  :disabled="klineRecords.length === 0"
                  @click="toggleKlinePlayback"
                >
                  <template #icon>
                    <icon-pause v-if="klinePlaying" />
                    <icon-play-arrow v-else />
                  </template>
                </a-button>
              </a-tooltip>
              <span>数量</span>
              <a-input-number
                v-model="klineLimit"
                class="kline-limit-input"
                size="small"
                :min="klineLimitMin"
                :max="klineLimitMax"
                :step="50"
                :precision="0"
              />
              <a-button size="small" type="outline" :loading="klineLoading" @click="reloadKlineRecords">
                <template #icon><icon-refresh /></template>
                应用
              </a-button>
            </div>
            <div v-if="klineLatest" class="kline-price-strip">
              <span class="kline-last-price" :class="klineChangeClass">{{ formatKlineNumber(klineLatest.close) }}</span>
              <span :class="klineChangeClass">{{ klineChangeText }}</span>
            </div>
          </div>
          <div v-if="klineRecords.length > 0" ref="klineChartHost" class="kline-chart-host"></div>
          <a-empty v-else description="当前结果缺少 open/high/low/close 字段，无法生成K线图" />
        </div>
      </a-spin>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue';
import { Message } from '@arco-design/web-vue';
import { TooltipShowRule, TooltipShowType, dispose as disposeKlineChartInstance, init as initKlineChart } from 'klinecharts';
import type { Chart } from 'klinecharts';
import { listDatasetColumns, listDatasets, listFactors, listFields, listViewColumns, listViews } from '@/api/storage/metadata';
import { queryTimeSeriesRows, rebuildRecordView, rebuildTimeSeriesView, searchRecordRows } from '@/api/storage/view';
import type { Dataset, DatasetColumn, Factor, Field, FieldValueType, RecordRow, View, ViewColumn } from '@/api/storage/types';
import { useSpaceStore } from '@/store/modules/space';
import {
  adaptiveColumnWidth,
  recordRowsToTableRows,
  rowsToColumnNames,
  timeSeriesRowsToTableRows,
  type BrowseTableRow,
} from '@/views/data/browse/browse-utils';
import {
  buildKlineChartRecords,
  buildKlineQuerySorts,
  buildViewColumnLabels,
  buildViewFilterExprs,
  buildViewSorts,
  DEFAULT_KLINE_LIMIT,
  klineRowsHaveFreq,
  klineSubjectIdFromFilters,
  MAX_KLINE_LIMIT,
  MIN_KLINE_LIMIT,
  normalizeKlineLimit,
  type KlineChartRecord,
  viewDisplayName,
  viewModeFromPrimaryDataset,
  type ViewFilterOperator,
  type ViewFilterState,
  type ViewSortDirection,
} from './view-browse-utils';

defineOptions({ name: 'DataViewBrowse' });

type ViewBrowseTableRow = BrowseTableRow & { freq?: string };
type FilterFieldOption = { label: string; value: string; valueType: FieldValueType };

const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);

const views = ref<View[]>([]);
const datasets = ref<Dataset[]>([]);
const viewColumns = ref<ViewColumn[]>([]);
const datasetColumns = ref<DatasetColumn[]>([]);
const fields = ref<Field[]>([]);
const factors = ref<Factor[]>([]);
const activeViewId = ref('');
const tableRows = ref<ViewBrowseTableRow[]>([]);
const tableColumnNames = ref<string[]>([]);
const detailRow = ref<ViewBrowseTableRow>();
const detailVisible = ref(false);
const recordKeyword = ref('');
const filters = ref<ViewFilterState[]>([]);
const sortState = reactive<{ fieldName: string; direction: ViewSortDirection }>({ fieldName: '', direction: '' });
const metaLoading = ref(false);
const contextLoading = ref(false);
const loading = ref(false);
const rebuildLoading = ref(false);
const queryError = ref('');
const hasQueried = ref(false);
const klineVisible = ref(false);
const klineChartHost = ref<HTMLElement>();
const klineSubjectId = ref('');
const klineFreq = ref('');
const klineRecords = ref<KlineChartRecord[]>([]);
const klineLoading = ref(false);
const klineLimit = ref(DEFAULT_KLINE_LIMIT);
const klinePlaying = ref(false);
const klinePlaybackCursor = ref(0);
let klineChart: Chart | null = null;
let klineResizeObserver: ResizeObserver | null = null;
let klinePlaybackTimer: ReturnType<typeof setInterval> | null = null;
const KLINE_PLAYBACK_INTERVAL_MS = 320;

const pagination = reactive({
  current: 1,
  pageSize: 50,
  total: 0,
});

const filterOperatorOptions: Array<{ label: string; value: ViewFilterOperator }> = [
  { label: '% 类似', value: 'like' },
  { label: 'ABC 开头等于', value: 'prefix' },
  { label: 'ABC 结尾等于', value: 'suffix' },
  { label: '= 等于', value: 'eq' },
  { label: '≠ 不等于', value: 'neq' },
  { label: '⊂ 包含', value: 'contains' },
  { label: '⊄ 不包含', value: 'not_contains' },
  { label: '↔ 范围', value: 'range' },
  { label: '○ 为空', value: 'empty' },
  { label: 'Ø 非空', value: 'not_empty' },
];
const filterOperatorSymbols: Record<ViewFilterOperator, string> = {
  like: '%',
  prefix: 'Ab',
  suffix: 'bA',
  eq: '=',
  neq: '≠',
  contains: '⊂',
  not_contains: '⊄',
  range: '↔',
  empty: '○',
  not_empty: 'Ø',
};

const activeView = computed(() => views.value.find((item) => item.view_id === activeViewId.value));
const primaryDataset = computed(() => datasets.value.find((item) => item.dataset_id === activeView.value?.primary_dataset_id));
const currentDatasetName = computed(() => {
  const dataset = primaryDataset.value;
  if (!dataset) return activeView.value?.primary_dataset_id || '-';
  return dataset.name ? `${dataset.name} (${dataset.dataset_id})` : dataset.dataset_id;
});

const mode = computed(() => viewModeFromPrimaryDataset(datasets.value, activeView.value?.primary_dataset_id));
const modeText = computed(() => {
  if (mode.value === 'time_series') return '时序视图 / DuckDB';
  if (mode.value === 'record') return '记录视图 / Bleve';
  return '未知类型';
});

const tablePagination = computed(() => ({
  current: pagination.current,
  pageSize: pagination.pageSize,
  total: pagination.total,
  showTotal: true,
  showPageSize: true,
  showJumper: true,
  hideOnSinglePage: false,
  pageSizeOptions: [20, 50, 100, 200],
}));

const preferredColumnNames = computed(() => viewColumns.value.map((item) => item.column_name).filter(Boolean));
const columnLabels = computed(() =>
  buildViewColumnLabels(viewColumns.value, datasetColumns.value, fields.value, factors.value, datasets.value, activeView.value),
);
const filterFieldOptions = computed(() => {
  const options: FilterFieldOption[] = [];
  const seen = new Set<string>();
  const push = (value: string, label: string, valueType: FieldValueType) => {
    if (!value || seen.has(value)) return;
    seen.add(value);
    options.push({ value, label, valueType });
  };
  if (mode.value === 'time_series') {
    push('subject_id', '数据ID', 'FIELD_VALUE_TYPE_STRING');
    push('freq', '频率', 'FIELD_VALUE_TYPE_STRING');
    push('data_time', '时间', 'FIELD_VALUE_TYPE_TIME');
  } else if (mode.value === 'record') {
    push('record_id', '记录ID', 'FIELD_VALUE_TYPE_STRING');
    push('version', '版本', 'FIELD_VALUE_TYPE_STRING');
  }
  for (const column of viewColumns.value) {
    push(column.column_name, columnTitle(column.column_name), column.value_type || 'FIELD_VALUE_TYPE_STRING');
  }
  return options;
});
const detailColumns = computed(() => {
  const row = detailRow.value;
  if (!row) return [];
  const names = rowsToColumnNames([rowToSyntheticRecord(row)], tableColumnNames.value);
  return names.map((name) => ({ name: columnTitle(name), value: row.values[name] || '-' }));
});
const klineTitle = computed(() => (klineSubjectId.value ? `${klineSubjectId.value} K线` : 'K线图'));
const klineCandleCount = computed(() => klineRecords.value.length);
const normalizedKlineLimit = computed(() => normalizeKlineLimit(klineLimit.value));
const klineLimitMin = MIN_KLINE_LIMIT;
const klineLimitMax = MAX_KLINE_LIMIT;
const klineLatest = computed(() => klineRecords.value[klineRecords.value.length - 1]);
const klinePrevious = computed(() => klineRecords.value[klineRecords.value.length - 2]);
const klinePriceChange = computed(() => {
  if (!klineLatest.value || !klinePrevious.value) return 0;
  return klineLatest.value.close - klinePrevious.value.close;
});
const klinePriceChangePercent = computed(() => {
  if (!klinePrevious.value?.close) return 0;
  return (klinePriceChange.value / klinePrevious.value.close) * 100;
});
const klineChangeClass = computed(() => {
  if (klinePriceChange.value > 0) return 'is-up';
  if (klinePriceChange.value < 0) return 'is-down';
  return 'is-flat';
});
const klineChangeText = computed(() => {
  const sign = klinePriceChange.value > 0 ? '+' : '';
  return `${sign}${formatKlineNumber(klinePriceChange.value)} (${sign}${klinePriceChangePercent.value.toFixed(2)}%)`;
});

async function loadMeta() {
  const space_id = selectedSpaceId.value;
  if (!space_id) return;
  metaLoading.value = true;
  try {
    const page = { page: 1, size: 1000 };
    const [viewRsp, datasetRsp, fieldRsp, factorRsp] = await Promise.all([
      listViews({ space_id, page }),
      listDatasets({ space_id, page }),
      listFields({ space_id, page }),
      listFactors({ space_id, page }),
    ]);
    views.value = viewRsp.views || [];
    datasets.value = datasetRsp.datasets || [];
    fields.value = fieldRsp.fields || [];
    factors.value = factorRsp.factors || [];
    ensureSelectedView();
    await loadViewContext();
  } catch (error) {
    Message.error(error instanceof Error ? error.message : '加载视图失败');
  } finally {
    metaLoading.value = false;
  }
}

function ensureSelectedView() {
  if (!views.value.length) {
    activeViewId.value = '';
    return;
  }
  if (!activeViewId.value || !views.value.some((item) => item.view_id === activeViewId.value)) {
    activeViewId.value = views.value[0].view_id;
  }
}

async function onViewChange() {
  clearViewState();
  await loadViewContext();
}

function clearViewState() {
  viewColumns.value = [];
  datasetColumns.value = [];
  tableRows.value = [];
  tableColumnNames.value = [];
  filters.value = [];
  resetSortState();
  detailRow.value = undefined;
  closeKlineModal();
  queryError.value = '';
  hasQueried.value = false;
  pagination.current = 1;
  pagination.total = 0;
}

async function loadViewContext() {
  const space_id = selectedSpaceId.value;
  const view = activeView.value;
  if (!space_id || !view) return;

  contextLoading.value = true;
  try {
    const columnsRsp = await listViewColumns({ space_id, view_id: view.view_id, page: { page: 1, size: 1000 } });
    viewColumns.value = columnsRsp.columns || [];
    await loadDatasetColumns(space_id, view);
    resetFilterRows();
    resetSortState();
    await reloadRows();
  } catch (error) {
    Message.error(error instanceof Error ? error.message : '加载视图上下文失败');
  } finally {
    contextLoading.value = false;
  }
}

async function loadDatasetColumns(space_id: string, view: View) {
  const datasetIds = new Set([view.primary_dataset_id, ...(view.dataset_ids || [])].filter(Boolean));
  const results = await Promise.all(
    Array.from(datasetIds).map((dataset_id) =>
      listDatasetColumns({ space_id, dataset_id, page: { page: 1, size: 1000 } }),
    ),
  );
  datasetColumns.value = results.flatMap((rsp) => rsp.columns || []);
}

async function reloadRows() {
  tableColumnNames.value = preferredColumnNames.value;
  if (!activeView.value) return;
  if (mode.value === 'time_series') {
    await loadTimeSeriesViewRows();
    return;
  }
  if (mode.value === 'record') {
    await loadRecordViewRows();
  }
}

async function loadTimeSeriesViewRows() {
  const space_id = spaceStore.requireSpaceId();
  const view = activeView.value;
  if (!view) return;
  loading.value = true;
  queryError.value = '';
  try {
    const rsp = await queryTimeSeriesRows({
      space_id,
      view_id: view.view_id,
      filters: activeFilterExprs(),
      sorts: buildViewSorts(sortState),
      page: { page: pagination.current, size: pagination.pageSize },
    });
    const rows = rsp.rows || [];
    tableRows.value = timeSeriesRowsToTableRows(rows).map((row, index) => ({
      ...row,
      freq: rows[index]?.key?.freq || '-',
    }));
    tableColumnNames.value = rowsToColumnNames(rows, preferredColumnNames.value);
    pagination.total = rsp.page_result?.total || rows.length;
    hasQueried.value = true;
  } catch (error) {
    queryError.value = error instanceof Error ? error.message : '查询时序视图失败';
    tableRows.value = [];
    pagination.total = 0;
    hasQueried.value = true;
    Message.error(queryError.value);
  } finally {
    loading.value = false;
  }
}

async function loadRecordViewRows() {
  const space_id = spaceStore.requireSpaceId();
  const view = activeView.value;
  if (!view) return;
  loading.value = true;
  queryError.value = '';
  try {
    const rsp = await searchRecordRows({
      space_id,
      view_id: view.view_id,
      text_query: recordKeyword.value.trim(),
      filters: activeFilterExprs(),
      sorts: buildViewSorts(sortState),
      page: { page: pagination.current, size: pagination.pageSize },
    });
    const rows = rsp.rows || [];
    tableRows.value = recordRowsToTableRows(rows);
    tableColumnNames.value = rowsToColumnNames(rows, preferredColumnNames.value);
    pagination.total = rsp.page_result?.total || rows.length;
    hasQueried.value = true;
  } catch (error) {
    queryError.value = error instanceof Error ? error.message : '查询记录视图失败';
    tableRows.value = [];
    pagination.total = 0;
    hasQueried.value = true;
    Message.error(queryError.value);
  } finally {
    loading.value = false;
  }
}

async function rebuildActiveView() {
  const view = activeView.value;
  if (!view) return;
  const space_id = spaceStore.requireSpaceId();
  rebuildLoading.value = true;
  try {
    if (mode.value === 'time_series') {
      await rebuildTimeSeriesView({ space_id, view_id: view.view_id });
    } else if (mode.value === 'record') {
      await rebuildRecordView({ space_id, view_id: view.view_id });
    } else {
      throw new Error('无法识别该视图的主数据集类型');
    }
    Message.success('视图重建任务已提交');
    await loadMeta();
  } catch (error) {
    Message.error(error instanceof Error ? error.message : '重建视图失败');
  } finally {
    rebuildLoading.value = false;
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
  recordKeyword.value = '';
  resetSortState();
  resetFilterRows();
  pagination.current = 1;
  await reloadRows();
}

function resetSortState() {
  if (mode.value === 'time_series') {
    sortState.fieldName = 'data_time';
    sortState.direction = 'desc';
    return;
  }
  sortState.fieldName = '';
  sortState.direction = '';
}

function resetFilterRows() {
  filters.value = filterFieldOptions.value.map((option) => createFilterState(option));
}

function createFilterState(option?: FilterFieldOption): ViewFilterState {
  return {
    fieldName: option?.value || '',
    operator: 'contains',
    valueType: option?.valueType || 'FIELD_VALUE_TYPE_STRING',
    value: '',
    startValue: '',
    endValue: '',
  };
}

function setFilterOperator(filter: ViewFilterState, value: string | number | Record<string, unknown> | undefined) {
  const next = typeof value === 'string' ? value : '';
  if (!isViewFilterOperator(next)) return;
  filter.operator = next;
  if (next === 'empty' || next === 'not_empty') {
    filter.value = '';
    filter.startValue = '';
    filter.endValue = '';
  } else if (next === 'range') {
    filter.value = '';
  } else {
    filter.startValue = '';
    filter.endValue = '';
  }
}

function isViewFilterOperator(value: string): value is ViewFilterOperator {
  return filterOperatorOptions.some((option) => option.value === value);
}

function filterOperatorSymbol(operator: ViewFilterOperator) {
  return filterOperatorSymbols[operator] || '%';
}

function filterOperatorTitle(operator: ViewFilterOperator) {
  return filterOperatorOptions.find((option) => option.value === operator)?.label || '检索类型';
}

function filterFieldLabel(fieldName: string) {
  return filterFieldOptions.value.find((item) => item.value === fieldName)?.label || columnTitle(fieldName);
}

function activeFilterExprs() {
  return buildViewFilterExprs(
    filters.value.map((filter) => ({
      ...filter,
      valueType: filterValueType(filter.fieldName),
    })),
  );
}

function filterValueType(fieldName: string): FieldValueType {
  return filterFieldOptions.value.find((item) => item.value === fieldName)?.valueType || 'FIELD_VALUE_TYPE_STRING';
}

async function onPageChange(page: number) {
  pagination.current = page;
  await reloadRows();
}

async function onPageSizeChange(pageSize: number) {
  pagination.pageSize = pageSize;
  pagination.current = 1;
  await reloadRows();
}

function columnTitle(columnName: string) {
  return columnLabels.value[columnName] || columnName;
}

function dynamicColumnWidth(columnName: string) {
  return adaptiveColumnWidth(columnName, columnTitle(columnName), tableRows.value);
}

async function setSort(fieldName: string, direction: ViewSortDirection) {
  if (sortState.fieldName === fieldName && sortState.direction === direction) {
    sortState.fieldName = '';
    sortState.direction = '';
  } else {
    sortState.fieldName = fieldName;
    sortState.direction = direction;
  }
  pagination.current = 1;
  await reloadRows();
}

function sortArrowClass(fieldName: string, direction: ViewSortDirection) {
  return {
    'sort-arrow': true,
    active: sortState.fieldName === fieldName && sortState.direction === direction,
  };
}

function openDetail(row: ViewBrowseTableRow) {
  detailRow.value = row;
  detailVisible.value = true;
}

async function openKlineModal() {
  const subjectId = klineSubjectIdFromFilters(filters.value);
  if (!subjectId) {
    Message.warning('请先在数据ID检索框输入要查看的标的');
    return;
  }

  klineSubjectId.value = subjectId;
  const ok = await loadKlineRecords(subjectId);
  if (!ok) return;
  klineVisible.value = true;
  nextTick(renderKlineChart);
}

async function reloadKlineRecords() {
  const subjectId = klineSubjectId.value || klineSubjectIdFromFilters(filters.value);
  if (!subjectId) {
    Message.warning('请先在数据ID检索框输入要查看的标的');
    return;
  }
  klineSubjectId.value = subjectId;
  const ok = await loadKlineRecords(subjectId);
  if (ok && klineVisible.value) {
    nextTick(renderKlineChart);
  }
}

async function loadKlineRecords(subjectId: string) {
  const view = activeView.value;
  if (!view) return false;
  stopKlinePlayback(false);
  klineLoading.value = true;
  try {
    const rows = await fetchKlineTableRows(view.view_id);
    if (!klineRowsHaveFreq(rows)) {
      Message.warning('当前查询结果缺少 freq 字段，无法展示K线');
      return false;
    }

    const records = buildKlineChartRecords(rows, subjectId);
    if (records.length === 0) {
      Message.warning('当前结果缺少 open/high/low/close 字段，无法生成K线图');
      return false;
    }

    klineLimit.value = normalizedKlineLimit.value;
    klineFreq.value = firstKlineFreq(subjectId, rows);
    klineRecords.value = records;
    klinePlaybackCursor.value = records.length;
    return true;
  } catch (error) {
    Message.error(error instanceof Error ? error.message : '加载K线数据失败');
    return false;
  } finally {
    klineLoading.value = false;
  }
}

async function fetchKlineTableRows(viewId: string): Promise<ViewBrowseTableRow[]> {
  const space_id = spaceStore.requireSpaceId();
  const rsp = await queryTimeSeriesRows({
    space_id,
    view_id: viewId,
    filters: activeFilterExprs(),
    sorts: buildKlineQuerySorts(),
    page: { page: 1, size: normalizedKlineLimit.value },
  });
  const rows = rsp.rows || [];
  return timeSeriesRowsToTableRows(rows).map((row, index) => ({
    ...row,
    freq: rows[index]?.key?.freq || '-',
  }));
}

function closeKlineModal() {
  klineVisible.value = false;
  stopKlinePlayback(false);
  disposeKlineChart();
  klineRecords.value = [];
  klineSubjectId.value = '';
  klineFreq.value = '';
  klinePlaybackCursor.value = 0;
}

function firstKlineFreq(subjectId: string, rows = tableRows.value) {
  const row = rows.find((item) => item.key === subjectId && item.freq && item.freq !== '-')
    || rows.find((item) => item.freq && item.freq !== '-');
  return row?.freq || '-';
}

function renderKlineChart() {
  const host = klineChartHost.value;
  const records = klineRecords.value;
  if (!klineVisible.value || !host || records.length === 0) return;

  disposeKlineChart();
  klineChart = initKlineChart(host, {
    locale: 'zh-CN',
    timezone: 'Asia/Shanghai',
    styles: {
      grid: {
        horizontal: { color: 'rgba(160, 174, 192, 0.12)' },
        vertical: { color: 'rgba(160, 174, 192, 0.12)' },
      },
      candle: {
        bar: {
          upColor: '#ef5350',
          downColor: '#26a69a',
          noChangeColor: '#9ba8b7',
          upBorderColor: '#ef5350',
          downBorderColor: '#26a69a',
          noChangeBorderColor: '#9ba8b7',
          upWickColor: '#ef5350',
          downWickColor: '#26a69a',
          noChangeWickColor: '#9ba8b7',
        },
        tooltip: {
          showRule: TooltipShowRule.None,
          showType: TooltipShowType.Standard,
          text: { color: '#d9e1ec', size: 12 },
        },
      },
      indicator: {
        tooltip: {
          showRule: TooltipShowRule.None,
          showName: true,
          showParams: true,
          text: { color: '#9ba8b7', size: 12 },
        },
      },
      xAxis: {
        axisLine: { color: 'rgba(160, 174, 192, 0.22)' },
        tickText: { color: '#8c9bab' },
      },
      yAxis: {
        axisLine: { color: 'rgba(160, 174, 192, 0.22)' },
        tickText: { color: '#8c9bab' },
      },
      separator: {
        color: '#222b35',
      },
      crosshair: {
        horizontal: { line: { color: 'rgba(217, 225, 236, 0.45)' }, text: { backgroundColor: '#263241' } },
        vertical: { line: { color: 'rgba(217, 225, 236, 0.45)' }, text: { backgroundColor: '#263241' } },
      },
    },
  });
  if (!klineChart) {
    Message.error('K线图初始化失败');
    return;
  }

  klineChart.setPriceVolumePrecision(8, 2);
  klineChart.setBarSpace(10);
  klineChart.applyNewData(records);
  klineChart.createIndicator('VOL', false, { height: 112, minHeight: 84 });
  klinePlaybackCursor.value = records.length;

  klineResizeObserver = new ResizeObserver(([entry]) => {
    const nextWidth = Math.floor(entry.contentRect.width);
    const nextHeight = Math.floor(entry.contentRect.height);
    if (klineChart && nextWidth > 0 && nextHeight > 0) {
      klineChart.resize();
    }
  });
  klineResizeObserver.observe(host);
}

function toggleKlinePlayback() {
  if (klinePlaying.value) {
    stopKlinePlayback(false);
    return;
  }
  startKlinePlayback();
}

function startKlinePlayback() {
  if (!klineChart || klineRecords.value.length === 0) return;
  stopKlinePlayback(false);
  if (klinePlaybackCursor.value <= 0 || klinePlaybackCursor.value >= klineRecords.value.length) {
    klinePlaybackCursor.value = 1;
  }
  klinePlaying.value = true;
  applyKlinePlaybackFrame();
  klinePlaybackTimer = setInterval(() => {
    if (!klinePlaying.value) return;
    if (klinePlaybackCursor.value >= klineRecords.value.length) {
      stopKlinePlayback(false);
      return;
    }
    klinePlaybackCursor.value += 1;
    applyKlinePlaybackFrame();
    if (klinePlaybackCursor.value >= klineRecords.value.length) {
      stopKlinePlayback(false);
    }
  }, KLINE_PLAYBACK_INTERVAL_MS);
}

function stopKlinePlayback(renderFull = false) {
  if (klinePlaybackTimer) {
    clearInterval(klinePlaybackTimer);
    klinePlaybackTimer = null;
  }
  klinePlaying.value = false;
  if (renderFull && klineChart && klineRecords.value.length > 0) {
    klinePlaybackCursor.value = klineRecords.value.length;
    klineChart.applyNewData(klineRecords.value);
  }
}

function applyKlinePlaybackFrame() {
  if (!klineChart || klineRecords.value.length === 0) return;
  const cursor = Math.min(klineRecords.value.length, Math.max(1, klinePlaybackCursor.value));
  klineChart.applyNewData(klineRecords.value.slice(0, cursor));
}

function disposeKlineChart() {
  stopKlinePlayback(false);
  klineResizeObserver?.disconnect();
  klineResizeObserver = null;
  if (klineChart) {
    disposeKlineChartInstance(klineChart);
  }
  klineChart = null;
}

function formatKlineNumber(value: number) {
  return Number.isFinite(value) ? value.toLocaleString(undefined, { maximumFractionDigits: 8 }) : '-';
}

function rowToSyntheticRecord(row: ViewBrowseTableRow): RecordRow {
  return {
    key: { space_id: '', dataset_id: '', record_id: row.key, version: row.version },
    columns: Object.keys(row.values).map((name) => ({
      column_name: name,
      value_type: 'FIELD_VALUE_TYPE_STRING',
      value: { string_value: row.values[name] },
    })),
  };
}

onMounted(loadMeta);
onBeforeUnmount(disposeKlineChart);
watch(selectedSpaceId, () => {
  activeViewId.value = '';
  clearViewState();
  loadMeta();
});
watch(klineVisible, (visible) => {
  if (!visible) disposeKlineChart();
});
</script>

<style scoped>
.view-browse-page {
  width: 100%;
  height: 100%;
  min-width: 0;
  box-sizing: border-box;
  padding: 20px 20px 72px;
  padding-bottom: 72px;
  overflow-y: auto;
}

.view-browse-page :deep(.arco-spin) {
  display: block;
  width: 100%;
  min-width: 0;
}

.page-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 16px;
}

.page-head h2 {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
}

.view-tabs-row {
  min-width: 0;
  margin-bottom: 12px;
}

.view-tabs :deep(.arco-tabs-content) {
  display: none;
}

.view-status-line {
  display: flex;
  align-items: center;
  gap: 8px;
  min-height: 34px;
  margin-bottom: 12px;
  color: var(--color-text-3);
}

.query-alert {
  margin-bottom: 12px;
}

.view-query-panel {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: 14px 18px;
  align-items: start;
  margin-bottom: 12px;
  padding: 18px 20px;
  border: 1px solid var(--color-border-2);
  border-radius: 8px;
  background: var(--color-bg-2);
}

.filter-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
  gap: 14px 28px;
  min-width: 0;
}

.filter-item {
  display: grid;
  grid-template-columns: minmax(72px, max-content) minmax(0, 1fr);
  gap: 8px;
  align-items: center;
  min-width: 0;
}

.filter-item label {
  overflow: hidden;
  max-width: 84px;
  color: var(--color-text-2);
  font-weight: 500;
  text-align: right;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.filter-control {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 34px;
  gap: 8px;
  align-items: center;
  min-width: 0;
}

.range-filter-item {
  grid-column: span 2;
}

.range-filter-item .filter-control {
  grid-template-columns: minmax(0, 1fr) auto minmax(0, 1fr) 34px;
}

.empty-filter-control {
  grid-template-columns: minmax(0, 1fr) 34px;
}

.range-separator {
  color: var(--color-text-3);
}

.empty-filter-placeholder {
  display: flex;
  align-items: center;
  height: 32px;
  padding: 0 12px;
  border: 1px solid var(--color-border-2);
  border-radius: 4px;
  color: var(--color-text-3);
  background: var(--color-fill-1);
}

.operator-button,
.operator-static {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 34px;
  height: 32px;
  border: 1px solid var(--color-border-2);
  border-radius: 6px;
  color: var(--color-text-2);
  font-weight: 600;
  background: var(--color-bg-1);
}

.operator-button {
  cursor: pointer;
}

.operator-button:hover {
  border-color: rgb(var(--primary-6));
  color: rgb(var(--primary-6));
}

.query-actions {
  display: flex;
  gap: 8px;
  align-items: center;
  justify-content: flex-end;
  min-width: 126px;
}

.result-pane {
  width: 100%;
  min-width: 0;
  max-width: 100%;
  box-sizing: border-box;
  padding: 12px;
  border: 1px solid var(--color-border-2);
  border-radius: 8px;
  background: var(--color-bg-2);
}

.result-pane :deep(.arco-pagination) {
  margin-top: 12px;
}

.record-search-bar {
  flex: 0 1 360px;
  min-width: 260px;
}

.detail-body {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.detail-table {
  margin-top: 4px;
}

.kline-modal-body {
  display: flex;
  flex-direction: column;
  gap: 12px;
  width: 100%;
  min-width: 0;
  box-sizing: border-box;
  overflow: hidden;
}

.kline-spin {
  display: block;
  width: 100%;
  min-width: 0;
}

.kline-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  min-height: 48px;
  width: 100%;
  padding: 10px 14px;
  border: 1px solid #222b35;
  border-radius: 8px;
  box-sizing: border-box;
  color: #d9e1ec;
  background: #151a21;
  overflow: hidden;
}

.kline-symbol-block,
.kline-control-strip,
.kline-price-strip {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 10px;
  min-width: 0;
}

.kline-symbol-block strong {
  color: #ffffff;
  font-size: 18px;
  font-weight: 700;
}

.kline-symbol-block span {
  color: #9ba8b7;
  font-size: 12px;
}

.kline-control-strip {
  justify-content: center;
  color: #9ba8b7;
  font-size: 12px;
}

.kline-control-strip :deep(.arco-btn-outline) {
  border-color: #2f3b4a;
  color: #d9e1ec;
  background: transparent;
}

.kline-play-button {
  flex: 0 0 auto;
}

.kline-play-button:not(:disabled):hover {
  border-color: #26a69a;
  color: #26a69a;
}

.kline-limit-input {
  width: 108px;
}

.kline-limit-input :deep(.arco-input-wrapper) {
  border-color: #2f3b4a;
  color: #d9e1ec;
  background: #101318;
}

.kline-price-strip {
  justify-content: flex-end;
  font-weight: 600;
}

.kline-last-price {
  font-size: 20px;
}

.is-up {
  color: #ef5350;
}

.is-down {
  color: #26a69a;
}

.is-flat {
  color: #9ba8b7;
}

.kline-chart-host {
  width: 100%;
  max-width: 100%;
  height: min(62vh, 560px);
  min-height: 420px;
  box-sizing: border-box;
  overflow: hidden;
  border: 1px solid #222b35;
  border-radius: 8px;
  background: #101318;
}

.sortable-title {
  display: inline-flex;
  gap: 6px;
  align-items: center;
  max-width: 100%;
  white-space: nowrap;
}

.sort-arrows {
  display: inline-flex;
  flex-direction: column;
  gap: 1px;
  width: 12px;
}

.sort-arrow {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 12px;
  height: 9px;
  padding: 0;
  border: 0;
  color: var(--color-text-4);
  font-size: 9px;
  line-height: 1;
  background: transparent;
  cursor: pointer;
}

.sort-arrow:hover,
.sort-arrow.active {
  color: rgb(var(--primary-6));
}

@media (max-width: 560px) {
  .page-head,
  .view-status-line {
    align-items: flex-start;
    flex-direction: column;
  }

  .view-query-panel {
    grid-template-columns: 1fr;
  }

  .filter-grid {
    grid-template-columns: 1fr;
  }

  .range-filter-item {
    grid-column: span 1;
  }

  .query-actions {
    justify-content: flex-start;
  }

  .kline-toolbar {
    align-items: flex-start;
    flex-direction: column;
  }

  .kline-price-strip {
    justify-content: flex-start;
  }

  .kline-control-strip {
    justify-content: flex-start;
  }

  .kline-chart-host {
    min-height: 360px;
  }

  .filter-item {
    grid-template-columns: 92px minmax(0, 1fr);
  }
}
</style>
