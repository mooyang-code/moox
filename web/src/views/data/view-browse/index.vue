<template>
  <div class="moox-page view-browse-page">
    <div class="moox-inner">
      <div v-if="!props.embedded" class="page-head">
        <div class="page-head__title">
          <slot name="page-title">
            <h2>{{ props.pageTitle }}</h2>
          </slot>
        </div>
      </div>

      <a-alert v-if="!selectedSpaceId" type="warning" show-icon>请先在顶部选择空间</a-alert>

      <a-spin v-else :loading="metaLoading">
        <a-empty v-if="visibleViews.length === 0" :description="props.emptyDescription" />

        <template v-else>
          <section class="view-tabs-row">
            <a-tabs v-model:active-key="activeViewId" type="rounded" size="medium" class="view-tabs" @change="onViewChange">
              <a-tab-pane v-for="view in visibleViews" :key="view.view_id" :title="viewDisplayName(view)" />
            </a-tabs>
          </section>

          <section class="view-status-line">
            <span>{{ currentDatasetName }}</span>
            <a-tag size="small" :color="mode === 'time_series' ? 'blue' : 'green'">{{ modeText }}</a-tag>
            <a-tag size="small" :color="activeView?.active_index_id ? 'green' : 'orange'">
              {{ activeView?.active_index_id ? "已构建" : "未构建" }}
            </a-tag>
            <span>{{ buildTimeText }}</span>
            <a-button type="text" size="mini" :loading="rebuildLogsLoading" @click="openRebuildLogs">日志</a-button>
            <a-button
              type="text"
              size="mini"
              :loading="rebuildRequestLoading"
              :disabled="!activeView || rebuildPollActive"
              @click="requestViewRebuild"
            >
              <template #icon><icon-refresh /></template>
              重建视图
            </a-button>
            <slot name="status-extra" />
            <span v-if="activeView?.active_view_revision">活跃版本 {{ activeView.active_view_revision }}</span>
          </section>

          <a-alert v-if="queryError" class="query-alert" type="error" show-icon>{{ queryError }}</a-alert>
          <a-alert v-else-if="hasQueried && !loading && tableRows.length === 0" class="query-alert" type="info" show-icon>
            当前视图查询成功，但结果为空。
          </a-alert>

          <QueryControls :loading="loading" :mode="mode">
            <section v-if="activeView" class="view-query-panel">
              <div class="filter-grid">
                <div v-if="mode === 'record'" class="filter-item record-keyword-filter">
                  <label>全文检索:</label>
                  <div class="filter-control">
                    <a-input v-model="recordKeyword" allow-clear placeholder="关键词" @press-enter="onRecordSearch" />
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
                  <div
                    class="filter-control"
                    :class="{ 'empty-filter-control': filter.operator === 'empty' || filter.operator === 'not_empty' }"
                  >
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
                    <span v-else class="empty-filter-placeholder">{{ filter.operator === "empty" ? "为空" : "非空" }}</span>
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
                <a-button
                  v-if="mode === 'time_series'"
                  size="small"
                  type="outline"
                  :loading="klineLoading"
                  @click="openKlineModal"
                >
                  <template #icon><icon-bar-chart /></template>
                  K线
                </a-button>
                <a-button size="small" @click="resetQueryControls">清空</a-button>
              </div>
            </section>
          </QueryControls>

          <ResultTable :loading="loading || contextLoading" :rows="tableRows">
            <section v-if="mode === 'time_series'" class="result-pane">
              <a-table
                row-key="id"
                size="small"
                :bordered="{ cell: true }"
                :loading="loading || contextLoading"
                :data="tableRows"
                :pagination="false"
                :scroll="{ x: 'max-content', y: 500 }"
              >
                <template #columns>
                  <a-table-column title="序号" :width="72" align="center" fixed="left">
                    <template #cell="{ rowIndex }">{{
                      (pagination.current - 1) * DEFAULT_VIEW_PAGE_SIZE + rowIndex + 1
                    }}</template>
                  </a-table-column>
                  <a-table-column data-index="key" :width="180" fixed="left">
                    <template #title
                      ><span class="sortable-title"
                        >数据ID<span class="sort-arrows"
                          ><button :class="sortArrowClass('subject_id', 'asc')" @click.stop="setSort('subject_id', 'asc')">
                            ▲</button
                          ><button :class="sortArrowClass('subject_id', 'desc')" @click.stop="setSort('subject_id', 'desc')">
                            ▼
                          </button></span
                        ></span
                      ></template
                    >
                  </a-table-column>
                  <a-table-column data-index="freq" :width="96">
                    <template #title
                      ><span class="sortable-title"
                        >频率<span class="sort-arrows"
                          ><button :class="sortArrowClass('freq', 'asc')" @click.stop="setSort('freq', 'asc')">▲</button
                          ><button :class="sortArrowClass('freq', 'desc')" @click.stop="setSort('freq', 'desc')">▼</button></span
                        ></span
                      ></template
                    >
                  </a-table-column>
                  <a-table-column data-index="seriesTag" :width="180">
                    <template #title
                      ><span class="sortable-title"
                        >序列标签<span class="sort-arrows"
                          ><button :class="sortArrowClass('series_tag', 'asc')" @click.stop="setSort('series_tag', 'asc')">
                            ▲</button
                          ><button :class="sortArrowClass('series_tag', 'desc')" @click.stop="setSort('series_tag', 'desc')">
                            ▼
                          </button></span
                        ></span
                      ></template
                    >
                  </a-table-column>
                  <a-table-column data-index="version" :width="230">
                    <template #title
                      ><span class="sortable-title"
                        >时间<span class="sort-arrows"
                          ><button :class="sortArrowClass('data_time', 'asc')" @click.stop="setSort('data_time', 'asc')">▲</button
                          ><button :class="sortArrowClass('data_time', 'desc')" @click.stop="setSort('data_time', 'desc')">
                            ▼
                          </button></span
                        ></span
                      ></template
                    >
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
                    <template #cell="{ record }">{{ record.values[column] || "-" }}</template>
                  </a-table-column>
                  <a-table-column title="操作" :width="92" align="center" fixed="right">
                    <template #cell="{ record }">
                      <a-button type="text" size="mini" @click="openDetail(record)">查看</a-button>
                    </template>
                  </a-table-column>
                </template>
              </a-table>
              <div v-if="hasQueried" class="preview-pager">
                <div class="preview-pager__actions">
                  <a-tooltip content="上一页">
                    <a-button
                      size="small"
                      shape="circle"
                      :disabled="pagination.current <= 1 || loading || contextLoading"
                      aria-label="上一页"
                      @click="onPreviewPrevPage"
                    >
                      <template #icon><icon-left /></template>
                    </a-button>
                  </a-tooltip>
                  <a-tooltip content="下一页">
                    <a-button
                      size="small"
                      shape="circle"
                      :disabled="!previewHasMore || loading || contextLoading"
                      aria-label="下一页"
                      @click="onPreviewNextPage"
                    >
                      <template #icon><icon-right /></template>
                    </a-button>
                  </a-tooltip>
                </div>
              </div>
            </section>

            <section v-else-if="mode === 'record'" class="result-pane">
              <a-table
                row-key="id"
                size="small"
                :bordered="{ cell: true }"
                :loading="loading || contextLoading"
                :data="tableRows"
                :pagination="false"
                :scroll="{ x: 'max-content', y: 460 }"
              >
                <template #columns>
                  <a-table-column title="序号" :width="72" align="center" fixed="left">
                    <template #cell="{ rowIndex }">{{
                      (pagination.current - 1) * DEFAULT_VIEW_PAGE_SIZE + rowIndex + 1
                    }}</template>
                  </a-table-column>
                  <a-table-column data-index="key" :width="200" fixed="left">
                    <template #title
                      ><span class="sortable-title"
                        >记录ID<span class="sort-arrows"
                          ><button :class="sortArrowClass('record_id', 'asc')" @click.stop="setSort('record_id', 'asc')">▲</button
                          ><button :class="sortArrowClass('record_id', 'desc')" @click.stop="setSort('record_id', 'desc')">
                            ▼
                          </button></span
                        ></span
                      ></template
                    >
                  </a-table-column>
                  <a-table-column data-index="version" :width="230">
                    <template #title
                      ><span class="sortable-title"
                        >版本<span class="sort-arrows"
                          ><button :class="sortArrowClass('version', 'asc')" @click.stop="setSort('version', 'asc')">▲</button
                          ><button :class="sortArrowClass('version', 'desc')" @click.stop="setSort('version', 'desc')">
                            ▼
                          </button></span
                        ></span
                      ></template
                    >
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
                    <template #cell="{ record }">{{ record.values[column] || "-" }}</template>
                  </a-table-column>
                  <a-table-column title="操作" :width="92" align="center" fixed="right">
                    <template #cell="{ record }">
                      <a-button type="text" size="mini" @click="openDetail(record)">查看</a-button>
                    </template>
                  </a-table-column>
                </template>
              </a-table>
              <div v-if="hasQueried" class="preview-pager">
                <div class="preview-pager__actions">
                  <a-tooltip content="上一页">
                    <a-button
                      size="small"
                      shape="circle"
                      :disabled="pagination.current <= 1 || loading || contextLoading"
                      aria-label="上一页"
                      @click="onPreviewPrevPage"
                    >
                      <template #icon><icon-left /></template>
                    </a-button>
                  </a-tooltip>
                  <a-tooltip content="下一页">
                    <a-button
                      size="small"
                      shape="circle"
                      :disabled="!previewHasMore || loading || contextLoading"
                      aria-label="下一页"
                      @click="onPreviewNextPage"
                    >
                      <template #icon><icon-right /></template>
                    </a-button>
                  </a-tooltip>
                </div>
              </div>
            </section>
          </ResultTable>

          <a-empty v-if="mode !== 'time_series' && mode !== 'record'" description="无法识别该视图的主数据集类型" />
        </template>
      </a-spin>
    </div>

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

    <a-modal v-model:visible="rebuildLogsVisible" title="视图构建日志" width="960px" :footer="false">
      <a-spin :loading="rebuildLogsLoading">
        <a-empty v-if="rebuildLogs.length === 0" description="暂无构建日志" />
        <a-table v-else :data="rebuildLogs" :pagination="false" :bordered="{ cell: true }" size="small">
          <template #columns>
            <a-table-column title="时间" :width="180">
              <template #cell="{ record }">{{
                formatRebuildLogTime(record.finished_at || record.created_at || record.started_at)
              }}</template>
            </a-table-column>
            <a-table-column title="原因" :width="130">
              <template #cell="{ record }">{{ rebuildReasonLabel(record.trigger_reason) }}</template>
            </a-table-column>
            <a-table-column title="结果" :width="100">
              <template #cell="{ record }">{{ rebuildResultLabel(record.result) }}</template>
            </a-table-column>
            <a-table-column title="耗时" :width="90">
              <template #cell="{ record }">{{ rebuildLogDuration(record) }}</template>
            </a-table-column>
            <a-table-column title="写入行数" :width="100">
              <template #cell="{ record }">{{ record.entries_written || 0 }}</template>
            </a-table-column>
            <a-table-column title="详情" :ellipsis="true" :tooltip="true">
              <template #cell="{ record }">{{ rebuildLogDetail(record) }}</template>
            </a-table-column>
          </template>
        </a-table>
        <div v-if="rebuildLogsPage.has_more" class="rebuild-logs-more">
          <a-button size="small" :loading="rebuildLogsLoading" @click="loadMoreRebuildLogs">加载更多</a-button>
        </div>
      </a-spin>
    </a-modal>

    <KlineModal
      v-model:visible="klineVisible"
      v-model:limit="klineLimit"
      :subject-id="klineSubjectId"
      :freq="klineFreq"
      :records="klineRecords"
      :loading="klineLoading"
      @reload="reloadKlineRecords"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from "vue";
import { Message, Modal } from "@arco-design/web-vue";
import {
  listDatasetColumns,
  listDatasets,
  listFactors,
  listFields,
  listViewColumns,
  listViewRebuildLogs,
  listViews,
  requestViewRebuild as requestViewRebuildTask
} from "@/api/storage/metadata";
import { queryTimeSeriesRows, searchRecordRows } from "@/api/storage/view";
import type {
  Dataset,
  DatasetColumn,
  Factor,
  Field,
  FieldValueType,
  RecordRow,
  TimeSeriesRow,
  View,
  ViewColumn,
  ViewRebuildLog
} from "@/api/storage/types";
import { useSpaceStore } from "@/store/modules/space";
import {
  adaptiveColumnWidth,
  recordRowsToTableRows,
  rowsToColumnNames,
  timeSeriesRowsToTableRows,
  type BrowseTableRow
} from "@/views/data/browse/browse-utils";
import {
  buildKlineChartRecords,
  buildKlineQuerySorts,
  buildViewColumnLabels,
  buildViewFilterExprs,
  buildViewSorts,
  DEFAULT_KLINE_LIMIT,
  exactSeriesTagFromFilters,
  klineRowsHaveFreq,
  klineSubjectIdFromFilters,
  normalizeKlineLimit,
  type KlineChartRecord,
  viewDisplayName,
  viewModeFromPrimaryDataset,
  type ViewFilterOperator,
  type ViewFilterState,
  type ViewSortDirection
} from "./view-browse-utils";
import {
  isLikelyFactorResultDataset,
  isLikelyFactorResultDatasetId,
  viewMatchesAttribution,
  type OwnerModule,
  type ViewRole
} from "@/views/data/shared/module-attribution";
import { viewBuildTimeLabel } from "./view-build-time";
import KlineModal from "./kline-modal.vue";
import QueryControls from "./components/query-controls.vue";
import ResultTable from "./components/result-table.vue";

defineOptions({ name: "DataViewBrowse" });

const props = withDefaults(
  defineProps<{
    embedded?: boolean;
    pageTitle?: string;
    emptyDescription?: string;
    allowedPrimaryDatasetIds?: string[];
    excludedPrimaryDatasetIds?: string[];
    viewOwnerModules?: OwnerModule[];
    viewRoles?: ViewRole[];
    includeUnowned?: boolean;
    excludeLikelyFactorDatasets?: boolean;
    autoRefreshIntervalMs?: number;
  }>(),
  {
    embedded: false,
    pageTitle: "视图数据浏览",
    emptyDescription: "暂无查询视图",
    allowedPrimaryDatasetIds: undefined,
    excludedPrimaryDatasetIds: undefined,
    viewOwnerModules: undefined,
    viewRoles: undefined,
    includeUnowned: false,
    excludeLikelyFactorDatasets: false,
    autoRefreshIntervalMs: 0
  }
);

type ViewBrowseTableRow = BrowseTableRow & { freq?: string };
type FilterFieldOption = { label: string; value: string; valueType: FieldValueType };

const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);

const views = ref<View[]>([]);
const datasets = ref<Dataset[]>([]);
const datasetById = computed(() => new Map(datasets.value.map(item => [item.dataset_id, item])));
const visibleViews = computed(() => {
  const allowedPrimaryDatasetIds = props.allowedPrimaryDatasetIds || [];
  const allowedPrimary = new Set(allowedPrimaryDatasetIds.filter(Boolean));
  const excludedPrimary = new Set((props.excludedPrimaryDatasetIds || []).filter(Boolean));
  return views.value.filter(view => {
    const matchedByDataset = allowedPrimary.size > 0 && allowedPrimary.has(view.primary_dataset_id);
    if (!matchedByDataset && excludedPrimary.has(view.primary_dataset_id)) {
      return false;
    }
    if (props.excludeLikelyFactorDatasets && !matchedByDataset && viewUsesLikelyFactorDataset(view)) {
      return false;
    }
    const matchedByAttrs = viewMatchesAttribution(view, {
      ownerModules: props.viewOwnerModules,
      viewRoles: props.viewRoles,
      includeUnowned: props.includeUnowned
    });
    if (allowedPrimary.size > 0 && (props.viewOwnerModules?.length || props.viewRoles?.length)) {
      return matchedByDataset || matchedByAttrs;
    }
    if (allowedPrimary.size > 0) {
      return matchedByDataset;
    }
    return matchedByAttrs;
  });
});
const viewColumns = ref<ViewColumn[]>([]);
const datasetColumns = ref<DatasetColumn[]>([]);
const fields = ref<Field[]>([]);
const factors = ref<Factor[]>([]);
const activeViewId = ref("");
const tableRows = ref<ViewBrowseTableRow[]>([]);
const tableColumnNames = ref<string[]>([]);
const detailRow = ref<ViewBrowseTableRow>();
const detailVisible = ref(false);
const rebuildLogsVisible = ref(false);
const rebuildLogsLoading = ref(false);
const rebuildLogs = ref<ViewRebuildLog[]>([]);
const rebuildLogsPage = ref<{ page?: number; has_more: boolean }>({ has_more: false });
const rebuildLogsRequestId = ref(0);
const rebuildRequestLoading = ref(false);
// Keep the action disabled for the whole asynchronous build, not just the
// short HTTP request. A second request would advance desired_view_revision
// and make the first A/B build stale.
const rebuildPollActive = ref(false);
const rebuildPollRevision = ref(0);
let rebuildPollTimer: ReturnType<typeof setTimeout> | undefined;
let rebuildPollDeadline = 0;
let rebuildPollToken = 0;
const recordKeyword = ref("");
const filters = ref<ViewFilterState[]>([]);
const sortState = reactive<{ fieldName: string; direction: ViewSortDirection }>({ fieldName: "", direction: "" });
const metaLoading = ref(false);
const contextLoading = ref(false);
const loading = ref(false);
const queryError = ref("");
const hasQueried = ref(false);
const previewHasMore = ref(false);
const klineVisible = ref(false);
const klineSubjectId = ref("");
const klineFreq = ref("");
const klineRecords = ref<KlineChartRecord[]>([]);
const klineLoading = ref(false);
const klineLimit = ref(DEFAULT_KLINE_LIMIT);
let autoRefreshTimer: ReturnType<typeof setInterval> | undefined;
const VIEW_BROWSE_UNSCOPED_PREVIEW_WINDOW_HOURS = 24;
const VIEW_BROWSE_SCOPED_PREVIEW_WINDOW_DAYS = 7;
const DEFAULT_VIEW_PAGE_SIZE = 25;
const KLINE_COLUMN_BASENAMES = ["open_time", "open", "high", "low", "close", "volume"];

const pagination = reactive({
  current: 1
});

const filterOperatorOptions: Array<{ label: string; value: ViewFilterOperator }> = [
  { label: "% 类似", value: "like" },
  { label: "ABC 开头等于", value: "prefix" },
  { label: "ABC 结尾等于", value: "suffix" },
  { label: "= 等于", value: "eq" },
  { label: "≠ 不等于", value: "neq" },
  { label: "⊂ 包含", value: "contains" },
  { label: "⊄ 不包含", value: "not_contains" },
  { label: "↔ 范围", value: "range" },
  { label: "○ 为空", value: "empty" },
  { label: "Ø 非空", value: "not_empty" }
];
const filterOperatorSymbols: Record<ViewFilterOperator, string> = {
  like: "%",
  prefix: "Ab",
  suffix: "bA",
  eq: "=",
  neq: "≠",
  contains: "⊂",
  not_contains: "⊄",
  range: "↔",
  empty: "○",
  not_empty: "Ø"
};

const activeView = computed(() => visibleViews.value.find(item => item.view_id === activeViewId.value));
const buildTimeText = computed(() => viewBuildTimeLabel(activeView.value));
const primaryDataset = computed(() => datasets.value.find(item => item.dataset_id === activeView.value?.primary_dataset_id));
const currentDatasetName = computed(() => {
  const dataset = primaryDataset.value;
  if (!dataset) return activeView.value?.primary_dataset_id || "-";
  return dataset.name ? `${dataset.name} (${dataset.dataset_id})` : dataset.dataset_id;
});

const mode = computed(() => viewModeFromPrimaryDataset(datasets.value, activeView.value?.primary_dataset_id));
const modeText = computed(() => {
  if (mode.value === "time_series") return "时序视图 / DuckDB";
  if (mode.value === "record") return "记录视图 / Bleve";
  return "未知类型";
});

const preferredColumnNames = computed(() => viewColumns.value.map(item => item.column_name).filter(Boolean));
const klineColumnNames = computed(() => {
  const names = preferredColumnNames.value;
  const out: string[] = [];
  for (const basename of KLINE_COLUMN_BASENAMES) {
    const matched = names.find(name => name === basename || name.endsWith(`.${basename}`));
    if (matched) out.push(matched);
  }
  return out;
});
const columnLabels = computed(() =>
  buildViewColumnLabels(viewColumns.value, datasetColumns.value, fields.value, factors.value, datasets.value, activeView.value)
);
const filterFieldOptions = computed(() => {
  const options: FilterFieldOption[] = [];
  const seen = new Set<string>();
  const push = (value: string, label: string, valueType: FieldValueType) => {
    if (!value || seen.has(value)) return;
    seen.add(value);
    options.push({ value, label, valueType });
  };
  if (mode.value === "time_series") {
    push("subject_id", "数据ID", "FIELD_VALUE_TYPE_STRING");
    push("freq", "频率", "FIELD_VALUE_TYPE_STRING");
    push("series_tag", "序列标签", "FIELD_VALUE_TYPE_STRING");
    push("data_time", "时间", "FIELD_VALUE_TYPE_TIME");
  } else if (mode.value === "record") {
    push("record_id", "记录ID", "FIELD_VALUE_TYPE_STRING");
    push("version", "版本", "FIELD_VALUE_TYPE_STRING");
  }
  for (const column of viewColumns.value) {
    push(column.column_name, columnTitle(column.column_name), column.value_type || "FIELD_VALUE_TYPE_STRING");
  }
  return options;
});
const detailColumns = computed(() => {
  const row = detailRow.value;
  if (!row) return [];
  const names = rowsToColumnNames([rowToSyntheticRecord(row)], tableColumnNames.value);
  return names.map(name => ({ name: columnTitle(name), value: row.values[name] || "-" }));
});
const normalizedKlineLimit = computed(() => normalizeKlineLimit(klineLimit.value));

const rebuildReasonLabels: Record<string, string> = {
  "1": "首次构建",
  "2": "定义变更",
  "3": "活跃索引缺失",
  "4": "活跃索引异常",
  "5": "覆盖修复",
  "6": "容量限制",
  "7": "手动修复",
  "8": "中断重试",
  VIEW_REBUILD_TRIGGER_INITIAL_BUILD: "首次构建",
  VIEW_REBUILD_TRIGGER_DEFINITION_CHANGE: "定义变更",
  VIEW_REBUILD_TRIGGER_ACTIVE_MISSING: "活跃索引缺失",
  VIEW_REBUILD_TRIGGER_ACTIVE_INVALID: "活跃索引异常",
  VIEW_REBUILD_TRIGGER_COVERAGE_REPAIR: "覆盖修复",
  VIEW_REBUILD_TRIGGER_SIZE_LIMIT: "容量限制",
  VIEW_REBUILD_TRIGGER_MANUAL_REPAIR: "手动修复",
  VIEW_REBUILD_TRIGGER_INTERRUPTED_RETRY: "中断重试"
};
const rebuildResultLabels: Record<string, string> = {
  "1": "进行中",
  "2": "成功",
  "3": "失败",
  "4": "已跳过",
  VIEW_REBUILD_RESULT_RUNNING: "进行中",
  VIEW_REBUILD_RESULT_SUCCEEDED: "成功",
  VIEW_REBUILD_RESULT_FAILED: "失败",
  VIEW_REBUILD_RESULT_SKIPPED: "已跳过"
};
function isRunningRebuildLog(log: ViewRebuildLog) {
  return Number(log.result) === 1 || log.result === "VIEW_REBUILD_RESULT_RUNNING";
}

function rebuildReasonLabel(value: number | string) {
  return rebuildReasonLabels[String(value)] || "未知原因";
}
function rebuildResultLabel(value: number | string) {
  return rebuildResultLabels[String(value)] || "未知";
}
const rebuildPhaseLabels: Record<string, string> = {
  reconcile: "准备协调",
  prepare: "准备索引",
  backfill: "回溯写入",
  catch_up: "追平实时数据",
  activate: "切换索引",
  completed: "已完成",
  interrupted: "中断清理",
  preflight: "前置检查"
};
function rebuildLogPhase(log: ViewRebuildLog) {
  if (!log.details_json) return "";
  try {
    const details = JSON.parse(log.details_json) as { phase?: string };
    return details.phase ? rebuildPhaseLabels[details.phase] || details.phase : "";
  } catch {
    return "";
  }
}
function formatRebuildLogTime(value?: string) {
  if (!value) return "-";
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? value : date.toLocaleString("zh-CN");
}
function rebuildLogDetail(log: ViewRebuildLog) {
  if (log.result === 4 || String(log.result) === "4" || log.result === "VIEW_REBUILD_RESULT_SKIPPED") {
    return `${log.block_reason || "等待前置条件"}${Number(log.skip_count || 0) > 1 ? `（${log.skip_count} 次）` : ""}`;
  }
  if (log.error_summary) return log.error_summary;
  const phase = rebuildLogPhase(log);
  if (phase) return phase;
  return log.finished_at && log.started_at ? `写入 ${log.entries_written || 0} 条` : "构建中";
}
function rebuildLogDuration(log: ViewRebuildLog) {
  if (!log.started_at || !log.finished_at) return "-";
  const durationMs = new Date(log.finished_at).valueOf() - new Date(log.started_at).valueOf();
  if (!Number.isFinite(durationMs) || durationMs < 0) return "-";
  if (durationMs < 1000) return `${durationMs}ms`;
  return `${(durationMs / 1000).toFixed(1)}s`;
}

async function openRebuildLogs(reset = true, showModal = true) {
  const view = activeView.value;
  const space_id = selectedSpaceId.value;
  if (!view || !space_id) return;
  const requestId = ++rebuildLogsRequestId.value;
  if (showModal) rebuildLogsVisible.value = true;
  rebuildLogsLoading.value = true;
  if (reset) {
    rebuildLogs.value = [];
    rebuildLogsPage.value = { has_more: false };
  }
  try {
    const rsp = await listViewRebuildLogs({ space_id, view_id: view.view_id, page: { page: 1, size: 100 } });
    if (
      requestId !== rebuildLogsRequestId.value ||
      activeView.value?.view_id !== view.view_id ||
      selectedSpaceId.value !== space_id
    )
      return;
    rebuildLogs.value = rsp.logs || [];
    rebuildLogsPage.value = rsp.page_result || { has_more: false };
  } catch (error) {
    if (requestId !== rebuildLogsRequestId.value) return;
    rebuildLogs.value = [];
    Message.error(error instanceof Error ? error.message : "加载构建日志失败");
  } finally {
    if (requestId === rebuildLogsRequestId.value) rebuildLogsLoading.value = false;
  }
}

function stopRebuildPolling() {
  if (rebuildPollTimer) {
    clearTimeout(rebuildPollTimer);
    rebuildPollTimer = undefined;
  }
  rebuildPollToken += 1;
}

function scheduleRebuildLogPolling() {
  if (rebuildPollTimer || Date.now() >= rebuildPollDeadline) return;
  const token = rebuildPollToken;
  rebuildPollTimer = setTimeout(async () => {
    rebuildPollTimer = undefined;
    if (token !== rebuildPollToken) return;
    await openRebuildLogs(false, false);
    if (token !== rebuildPollToken) return;
    const revision = rebuildPollRevision.value;
    const current =
      rebuildLogs.value.find(log => Number(log.target_view_revision || 0) === revision) ||
      rebuildLogs.value.find(log => Number(log.target_view_revision || 0) > revision);
    if (current && !isRunningRebuildLog(current)) {
      if (current.result === 2 || current.result === "2" || current.result === "VIEW_REBUILD_RESULT_SUCCEEDED") {
        await loadMeta();
      }
      rebuildPollActive.value = false;
      return;
    }
    if (Date.now() >= rebuildPollDeadline) {
      rebuildPollActive.value = false;
      Message.warning("重建任务仍在后台运行，可在日志中查看最新进度");
      return;
    }
    scheduleRebuildLogPolling();
  }, 2000);
}

async function requestViewRebuild() {
  const view = activeView.value;
  const space_id = selectedSpaceId.value;
  if (!view || !space_id || rebuildRequestLoading.value) return;
  Modal.confirm({
    title: "确认重建视图",
    content: "将异步创建新的 A/B 索引。当前视图仍保持可读，完成后新索引会自动切换生效。是否继续？",
    okText: "开始重建",
    cancelText: "取消",
    onOk: async () => {
      rebuildRequestLoading.value = true;
      try {
        const rsp = await requestViewRebuildTask({ space_id, view_id: view.view_id });
        rebuildPollRevision.value = Number(rsp.view?.desired_view_revision || 0);
        stopRebuildPolling();
        rebuildPollDeadline = Date.now() + 15 * 60 * 1000;
        rebuildPollActive.value = true;
        Message.success("已提交重建任务");
        await openRebuildLogs();
        scheduleRebuildLogPolling();
      } catch (error) {
        Message.error(error instanceof Error ? error.message : "提交视图重建失败");
        throw error;
      } finally {
        rebuildRequestLoading.value = false;
      }
    }
  });
}

async function loadMoreRebuildLogs() {
  const view = activeView.value;
  const space_id = selectedSpaceId.value;
  if (!view || !space_id || !rebuildLogsPage.value.has_more || rebuildLogsLoading.value) return;
  const requestId = rebuildLogsRequestId.value;
  const nextPage = (rebuildLogsPage.value.page || 1) + 1;
  rebuildLogsLoading.value = true;
  try {
    const rsp = await listViewRebuildLogs({ space_id, view_id: view.view_id, page: { page: nextPage, size: 100 } });
    if (
      requestId !== rebuildLogsRequestId.value ||
      activeView.value?.view_id !== view.view_id ||
      selectedSpaceId.value !== space_id
    )
      return;
    rebuildLogs.value = rebuildLogs.value.concat(rsp.logs || []);
    rebuildLogsPage.value = rsp.page_result || { has_more: false };
  } catch (error) {
    if (requestId === rebuildLogsRequestId.value) Message.error(error instanceof Error ? error.message : "加载构建日志失败");
  } finally {
    if (requestId === rebuildLogsRequestId.value) rebuildLogsLoading.value = false;
  }
}

async function loadMeta() {
  const space_id = selectedSpaceId.value;
  if (!space_id) return;
  metaLoading.value = true;
  try {
    const page = { page: 1, size: 1000 };
    const [viewItems, datasetItems, fieldRsp, factorRsp] = await Promise.all([
      listAllViews(space_id),
      listAllDatasets(space_id),
      listFields({ space_id, page }),
      listFactors({ space_id, page })
    ]);
    views.value = viewItems;
    datasets.value = datasetItems;
    fields.value = fieldRsp.fields || [];
    factors.value = factorRsp.factors || [];
    ensureSelectedView();
    await loadViewContext();
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "加载视图失败");
  } finally {
    metaLoading.value = false;
  }
}

async function listAllViews(spaceId: string) {
  const views: View[] = [];
  const size = 500;
  for (let pageNo = 1; ; pageNo += 1) {
    const rsp = await listViews({ space_id: spaceId, page: { page: pageNo, size } });
    views.push(...(rsp.views || []));
    if (!rsp.page_result?.has_more || (rsp.views || []).length === 0) {
      return views;
    }
  }
}

async function listAllDatasets(spaceId: string) {
  const datasets: Dataset[] = [];
  const size = 500;
  for (let pageNo = 1; ; pageNo += 1) {
    const rsp = await listDatasets({ space_id: spaceId, page: { page: pageNo, size } });
    datasets.push(...(rsp.datasets || []));
    if (!rsp.page_result?.has_more || (rsp.datasets || []).length === 0) {
      return datasets;
    }
  }
}

function viewUsesLikelyFactorDataset(view: View) {
  const dataset = datasetById.value.get(view.primary_dataset_id);
  if (dataset) {
    return isLikelyFactorResultDataset(dataset);
  }
  return isLikelyFactorResultDatasetId(view.primary_dataset_id);
}

function ensureSelectedView() {
  if (!visibleViews.value.length) {
    activeViewId.value = "";
    return;
  }
  if (!activeViewId.value || !visibleViews.value.some(item => item.view_id === activeViewId.value)) {
    activeViewId.value = visibleViews.value[0].view_id;
  }
}

watch(
  () => visibleViews.value.map(item => item.view_id).join("|"),
  async () => {
    const current = activeViewId.value;
    ensureSelectedView();
    if (activeViewId.value !== current) {
      clearViewState();
      await loadViewContext();
    }
  }
);

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
  queryError.value = "";
  hasQueried.value = false;
  previewHasMore.value = false;
  pagination.current = 1;
  rebuildLogsVisible.value = false;
  rebuildLogs.value = [];
  rebuildLogsRequestId.value += 1;
  stopRebuildPolling();
  rebuildPollActive.value = false;
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
    Message.error(error instanceof Error ? error.message : "加载视图上下文失败");
  } finally {
    contextLoading.value = false;
  }
}

async function loadDatasetColumns(space_id: string, view: View) {
  const datasetIds = new Set([view.primary_dataset_id, ...(view.dataset_ids || [])].filter(Boolean));
  const results = await Promise.all(
    Array.from(datasetIds).map(dataset_id => listDatasetColumns({ space_id, dataset_id, page: { page: 1, size: 1000 } }))
  );
  datasetColumns.value = results.flatMap(rsp => rsp.columns || []);
}

async function reloadRows() {
  tableColumnNames.value = preferredColumnNames.value;
  if (!activeView.value) return;
  if (mode.value === "time_series") {
    await loadTimeSeriesViewRows();
    return;
  }
  if (mode.value === "record") {
    await loadRecordViewRows();
  }
}

async function refreshRowsInBackground() {
  if (!activeView.value || loading.value || contextLoading.value) return;
  await reloadRows();
}

async function loadTimeSeriesViewRows() {
  const space_id = spaceStore.requireSpaceId();
  const view = activeView.value;
  if (!view) return;
  loading.value = true;
  queryError.value = "";
  try {
    const timeRange = defaultTimeSeriesPreviewRange();
    const rsp = await queryTimeSeriesRows({
      space_id,
      view_id: view.view_id,
      ...(timeRange ? { time_range: timeRange } : {}),
      ...(preferredColumnNames.value.length > 0 ? { column_names: preferredColumnNames.value } : {}),
      filter: activeFilterExprs(),
      sorts: buildViewSorts(sortState),
      page: { page: pagination.current, size: DEFAULT_VIEW_PAGE_SIZE },
      total_mode: "NONE"
    });
    const rows = rsp.rows || [];
    tableRows.value = timeSeriesRowsToTableRows(rows).map((row, index) => ({
      ...row,
      freq: rows[index]?.key?.freq || "-",
      seriesTag: rows[index]?.key?.series_tag || ""
    }));
    tableColumnNames.value = rowsToColumnNames(rows, preferredColumnNames.value);
    previewHasMore.value = !!rsp.page_result?.has_more;
    hasQueried.value = true;
  } catch (error) {
    queryError.value = error instanceof Error ? error.message : "查询时序视图失败";
    tableRows.value = [];
    previewHasMore.value = false;
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
  queryError.value = "";
  try {
    const rsp = await searchRecordRows({
      space_id,
      view_id: view.view_id,
      text_query: recordKeyword.value.trim(),
      filter: activeFilterExprs(),
      sorts: buildViewSorts(sortState),
      page: { page: pagination.current, size: DEFAULT_VIEW_PAGE_SIZE }
    });
    const rows = rsp.rows || [];
    tableRows.value = recordRowsToTableRows(rows);
    tableColumnNames.value = rowsToColumnNames(rows, preferredColumnNames.value);
    previewHasMore.value = !!rsp.page_result?.has_more;
    hasQueried.value = true;
  } catch (error) {
    queryError.value = error instanceof Error ? error.message : "查询记录视图失败";
    tableRows.value = [];
    previewHasMore.value = false;
    hasQueried.value = true;
    Message.error(queryError.value);
  } finally {
    loading.value = false;
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
  await reloadRows();
}

function resetSortState() {
  // Results are time-series data. Show the newest calculated period first so
  // an old first page cannot make a live factor view look stalled.
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

function createFilterState(option?: FilterFieldOption): ViewFilterState {
  return {
    fieldName: option?.value || "",
    operator: option?.value === "series_tag" ? "eq" : "contains",
    valueType: option?.valueType || "FIELD_VALUE_TYPE_STRING",
    value: "",
    startValue: "",
    endValue: ""
  };
}

function setFilterOperator(filter: ViewFilterState, value: string | number | Record<string, unknown> | undefined) {
  const next = typeof value === "string" ? value : "";
  if (!isViewFilterOperator(next)) return;
  filter.operator = next;
  if (next === "empty" || next === "not_empty") {
    filter.value = "";
    filter.startValue = "";
    filter.endValue = "";
  } else if (next === "range") {
    filter.value = "";
  } else {
    filter.startValue = "";
    filter.endValue = "";
  }
}

function isViewFilterOperator(value: string): value is ViewFilterOperator {
  return filterOperatorOptions.some(option => option.value === value);
}

function filterOperatorSymbol(operator: ViewFilterOperator) {
  return filterOperatorSymbols[operator] || "%";
}

function filterOperatorTitle(operator: ViewFilterOperator) {
  return filterOperatorOptions.find(option => option.value === operator)?.label || "检索类型";
}

function filterFieldLabel(fieldName: string) {
  return filterFieldOptions.value.find(item => item.value === fieldName)?.label || columnTitle(fieldName);
}

function activeFilterExprs() {
  return buildViewFilterExprs(
    filters.value.map(filter => ({
      ...filter,
      valueType: filterValueType(filter.fieldName)
    }))
  );
}

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

function dynamicColumnWidth(columnName: string) {
  return adaptiveColumnWidth(columnName, columnTitle(columnName), tableRows.value);
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

function sortArrowClass(fieldName: string, direction: ViewSortDirection) {
  return {
    "sort-arrow": true,
    active: sortState.fieldName === fieldName && sortState.direction === direction
  };
}

function openDetail(row: ViewBrowseTableRow) {
  detailRow.value = row;
  detailVisible.value = true;
}

async function openKlineModal() {
  const subjectId = klineSubjectIdFromFilters(filters.value);
  if (!subjectId) {
    Message.warning("请先在数据ID检索框输入要查看的标的");
    return;
  }
  const seriesTag = exactSeriesTagFromFilters(filters.value);
  if (seriesTag === undefined) {
    Message.warning("请先精确选择序列标签；默认序列请选择为空");
    return;
  }

  klineSubjectId.value = subjectId;
  const ok = await loadKlineRecords(subjectId, seriesTag);
  if (!ok) return;
  klineVisible.value = true;
}

async function reloadKlineRecords() {
  const subjectId = klineSubjectId.value || klineSubjectIdFromFilters(filters.value);
  if (!subjectId) {
    Message.warning("请先在数据ID检索框输入要查看的标的");
    return;
  }
  const seriesTag = exactSeriesTagFromFilters(filters.value);
  if (seriesTag === undefined) {
    Message.warning("请先精确选择序列标签；默认序列请选择为空");
    return;
  }
  klineSubjectId.value = subjectId;
  await loadKlineRecords(subjectId, seriesTag);
}

async function loadKlineRecords(subjectId: string, seriesTag: string) {
  const view = activeView.value;
  if (!view) return false;
  klineLoading.value = true;
  try {
    const rows = await fetchKlineTableRows(view.view_id);
    if (!klineRowsHaveFreq(rows)) {
      Message.warning("当前查询结果缺少 freq 字段，无法展示K线");
      return false;
    }

    const records = buildKlineChartRecords(rows, subjectId, seriesTag);
    if (records.length === 0) {
      Message.warning("当前结果缺少 open/high/low/close 字段，无法生成K线图");
      return false;
    }

    klineLimit.value = normalizedKlineLimit.value;
    klineFreq.value = firstKlineFreq(subjectId, rows);
    klineRecords.value = records;
    return true;
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "加载K线数据失败");
    return false;
  } finally {
    klineLoading.value = false;
  }
}

async function fetchKlineTableRows(viewId: string): Promise<ViewBrowseTableRow[]> {
  const space_id = spaceStore.requireSpaceId();
  const timeRange = defaultTimeSeriesPreviewRange();
  const rows: TimeSeriesRow[] = [];
  const limit = normalizedKlineLimit.value;
  for (let pageNo = 1; rows.length < limit; pageNo += 1) {
    const rsp = await queryTimeSeriesRows({
      space_id,
      view_id: viewId,
      ...(timeRange ? { time_range: timeRange } : {}),
      ...(klineColumnNames.value.length > 0 ? { column_names: klineColumnNames.value } : {}),
      filter: activeFilterExprs(),
      sorts: buildKlineQuerySorts(),
      limit,
      page: { page: pageNo, size: DEFAULT_VIEW_PAGE_SIZE },
      total_mode: "NONE"
    });
    const pageRows = rsp.rows || [];
    rows.push(...pageRows);
    if (!rsp.page_result?.has_more || pageRows.length === 0) break;
  }
  return timeSeriesRowsToTableRows(rows).map((row, index) => ({
    ...row,
    freq: rows[index]?.key?.freq || "-",
    seriesTag: rows[index]?.key?.series_tag || ""
  }));
}

function firstKlineFreq(subjectId: string, rows = tableRows.value) {
  const row =
    rows.find(item => item.key === subjectId && item.freq && item.freq !== "-") ||
    rows.find(item => item.freq && item.freq !== "-");
  return row?.freq || "-";
}

function closeKlineModal() {
  klineVisible.value = false;
  klineRecords.value = [];
  klineSubjectId.value = "";
  klineFreq.value = "";
}

function rowToSyntheticRecord(row: ViewBrowseTableRow): RecordRow {
  return {
    key: { space_id: "", dataset_id: "", record_id: row.key, version: row.version },
    fields: Object.keys(row.values).map(name => ({
      field_id: name,
      value: { string_value: row.values[name] }
    }))
  };
}

onMounted(() => {
  loadMeta();
  const interval = props.autoRefreshIntervalMs || 0;
  if (interval > 0) {
    autoRefreshTimer = setInterval(() => {
      void refreshRowsInBackground();
    }, interval);
  }
});
onBeforeUnmount(() => {
  stopRebuildPolling();
  if (autoRefreshTimer) {
    clearInterval(autoRefreshTimer);
    autoRefreshTimer = undefined;
  }
});
watch(selectedSpaceId, () => {
  activeViewId.value = "";
  clearViewState();
  loadMeta();
});
</script>

<style scoped>
.view-browse-page {
  width: 100%;
  height: 100%;
  min-width: 0;
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
  margin-bottom: var(--moox-space-2);
}

.page-head__title {
  display: flex;
  align-items: center;
  min-width: 0;
}

.page-head h2 {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
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

.view-query-panel {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: 14px 18px;
  align-items: start;
  margin-bottom: var(--moox-space-3);
  padding: 18px var(--moox-space-5);
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
  gap: var(--moox-space-2);
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
  gap: var(--moox-space-2);
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
  padding: 0 var(--moox-space-3);
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
  gap: var(--moox-space-2);
  align-items: center;
  justify-content: flex-end;
  min-width: 126px;
}

.result-pane {
  width: 100%;
  min-width: 0;
  max-width: 100%;
  box-sizing: border-box;
  padding: var(--moox-space-3);
  border: 1px solid var(--color-border-2);
  border-radius: 8px;
  background: var(--color-bg-2);
}

.result-pane :deep(.arco-pagination) {
  margin-top: var(--moox-space-3);
}

.preview-pager {
  display: flex;
  flex-wrap: wrap;
  gap: 10px var(--moox-space-4);
  align-items: center;
  justify-content: flex-end;
  margin-top: var(--moox-space-3);
  color: var(--color-text-2);
  font-size: 13px;
}

.preview-pager__hint {
  min-width: 0;
}

.preview-pager__actions {
  display: inline-flex;
  align-items: center;
  gap: 10px;
}

.record-search-bar {
  flex: 0 1 360px;
  min-width: 260px;
}

.detail-body {
  display: flex;
  flex-direction: column;
  gap: var(--moox-space-4);
  max-height: min(680px, calc(100vh - 220px));
  padding-right: var(--moox-space-1);
  overflow-y: auto;
}

.detail-table {
  margin-top: var(--moox-space-1);
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

  .filter-item {
    grid-template-columns: 92px minmax(0, 1fr);
  }
}
</style>
