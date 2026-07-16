<template>
  <div class="moox-page data-browse-page">
    <div class="moox-inner">
      <div class="page-head">
        <div class="page-head__title">
          <slot name="page-title">
            <h2>{{ props.pageTitle }}</h2>
          </slot>
        </div>
        <a-button :disabled="!selectedSpaceId" :loading="metaLoading || contextLoading" @click="loadMeta">
          <template #icon><icon-refresh /></template>
          刷新
        </a-button>
      </div>

      <a-alert v-if="!selectedSpaceId" type="warning" show-icon>请先在顶部选择空间</a-alert>

      <a-spin v-else :loading="metaLoading">
        <a-empty v-if="visibleDatasets.length === 0" description="暂无数据集" />

        <template v-else>
          <section class="dataset-tabs-row">
            <a-tabs
              v-model:active-key="activeDatasetId"
              type="rounded"
              size="medium"
              class="dataset-tabs"
              @change="onDatasetChange"
            >
              <a-tab-pane v-for="dataset in visibleDatasets" :key="dataset.dataset_id" :title="datasetDisplayName(dataset)" />
            </a-tabs>

            <a-tabs
              v-if="mode === 'time_series' && freqOptions.length > 0"
              v-model:active-key="activeFreq"
              type="line"
              size="medium"
              class="freq-tabs"
              @change="onFreqChange"
            >
              <a-tab-pane v-for="freq in freqOptions" :key="freq" :title="freq" />
            </a-tabs>
          </section>

          <section v-if="mode === 'time_series'" class="browse-shell">
            <aside class="data-id-pane">
              <div class="pane-head">
                <strong>数据ID</strong>
                <span>{{ filteredDataIds.length }} 个</span>
              </div>
              <a-input v-model="dataIdKeyword" allow-clear placeholder="搜索数据ID">
                <template #prefix><icon-search /></template>
              </a-input>
              <div class="data-id-list" :class="{ loading: contextLoading }">
                <a-spin v-if="contextLoading" :size="24" tip="加载中..." />
                <a-empty v-else-if="filteredDataIds.length === 0" description="暂无数据ID" />
                <button
                  v-for="item in filteredDataIds"
                  v-else
                  :key="item.id"
                  class="data-id-item"
                  :class="{ active: item.id === activeDataId }"
                  @click="selectDataId(item.id)"
                >
                  {{ displayDataIdText(item) }}
                </button>
              </div>
            </aside>

            <main class="table-pane">
              <div class="table-toolbar">
                <div>
                  <div class="dataset-title-line">
                    <strong>{{ activeDataId || "未选择数据ID" }}</strong>
                    <span v-if="activeFreq" class="inline-freq">/ {{ activeFreq }}</span>
                  </div>
                </div>
                <a-button :disabled="!activeDataId" :loading="loading" @click="reloadRows">
                  <template #icon><icon-refresh /></template>
                  重新加载
                </a-button>
              </div>

              <a-table
                row-key="id"
                size="small"
                :bordered="{ cell: true }"
                :loading="loading"
                :data="tableRows"
                :pagination="timeSeriesTablePagination"
                :scroll="{ x: 'max-content', y: 430 }"
                @page-change="onPageChange"
                @page-size-change="onPageSizeChange"
              >
                <template #columns>
                  <a-table-column title="序号" :width="72" align="center" fixed="left">
                    <template #cell="{ rowIndex }">{{ (pagination.current - 1) * pagination.pageSize + rowIndex + 1 }}</template>
                  </a-table-column>
                  <a-table-column data-index="key" :width="180" fixed="left">
                    <template #title>
                      <span class="sortable-title">
                        数据ID
                        <span class="sort-arrows">
                          <button :class="sortArrowClass('subject_id', 'asc')" @click.stop="setSort('subject_id', 'asc')">
                            ▲
                          </button>
                          <button :class="sortArrowClass('subject_id', 'desc')" @click.stop="setSort('subject_id', 'desc')">
                            ▼
                          </button>
                        </span>
                      </span>
                    </template>
                  </a-table-column>
                  <a-table-column data-index="version" :width="230">
                    <template #title>
                      <span class="sortable-title">
                        时间
                        <span class="sort-arrows">
                          <button :class="sortArrowClass('data_time', 'asc')" @click.stop="setSort('data_time', 'asc')">▲</button>
                          <button :class="sortArrowClass('data_time', 'desc')" @click.stop="setSort('data_time', 'desc')">
                            ▼
                          </button>
                        </span>
                      </span>
                    </template>
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
              <div v-if="timeSeriesUsesPreviewPager" class="preview-pager">
                <span class="preview-pager__hint">{{ previewPagerText(DATA_BROWSE_PREVIEW_LIMIT) }}</span>
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
                  <span class="preview-pager__page">第 {{ pagination.current }} 页</span>
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
            </main>
          </section>

          <section v-else-if="mode === 'record'" class="record-table-pane">
            <div class="table-toolbar">
              <div>
                <strong>记录数据</strong>
                <span>{{ datasetDisplayName(currentDataset) }}</span>
              </div>
              <a-button :loading="loading" @click="reloadRows">
                <template #icon><icon-refresh /></template>
                重新加载
              </a-button>
            </div>

            <a-table
              row-key="id"
              size="small"
              :bordered="{ cell: true }"
              :loading="loading"
              :data="tableRows"
              :pagination="recordTablePagination"
              :scroll="{ x: 'max-content', y: 460 }"
              @page-change="onPageChange"
              @page-size-change="onPageSizeChange"
            >
              <template #columns>
                <a-table-column title="序号" :width="72" align="center" fixed="left">
                  <template #cell="{ rowIndex }">{{ (pagination.current - 1) * pagination.pageSize + rowIndex + 1 }}</template>
                </a-table-column>
                <a-table-column data-index="key" :width="180" fixed="left">
                  <template #title>
                    <span class="sortable-title">
                      记录ID
                      <span class="sort-arrows">
                        <button :class="sortArrowClass('record_id', 'asc')" @click.stop="setSort('record_id', 'asc')">▲</button>
                        <button :class="sortArrowClass('record_id', 'desc')" @click.stop="setSort('record_id', 'desc')">▼</button>
                      </span>
                    </span>
                  </template>
                </a-table-column>
                <a-table-column data-index="version" :width="160">
                  <template #title>
                    <span class="sortable-title">
                      版本
                      <span class="sort-arrows">
                        <button :class="sortArrowClass('version', 'asc')" @click.stop="setSort('version', 'asc')">▲</button>
                        <button :class="sortArrowClass('version', 'desc')" @click.stop="setSort('version', 'desc')">▼</button>
                      </span>
                    </span>
                  </template>
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
            <div v-if="recordUsesPreviewPager" class="preview-pager">
              <span class="preview-pager__hint">{{ previewPagerText(DATA_BROWSE_PREVIEW_LIMIT) }}</span>
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
                <span class="preview-pager__page">第 {{ pagination.current }} 页</span>
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

          <a-empty v-else description="无法识别该数据集的数据类型" />
        </template>
      </a-spin>
    </div>

    <a-modal v-model:visible="detailVisible" title="数据详情" width="820px" :footer="false">
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
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import {
  listDatasetColumns,
  listDatasets,
  listDatasetSubjects,
  listFactors,
  listFields,
  listSubjects
} from "@/api/storage/metadata";
import { readRecordRows, readTimeSeriesRows } from "@/api/storage/access";
import type { Dataset, DatasetColumn, Factor, Field, PageResult, RecordRow, SortOrder } from "@/api/storage/types";
import { isTimeSeriesDataKind } from "@/views/data/shared/metadata-utils";
import { datasetMatchesAttribution, type DatasetRole, type OwnerModule } from "@/views/data/shared/module-attribution";
import { useSpaceStore } from "@/store/modules/space";
import {
  adaptiveColumnWidth,
  buildColumnLabels,
  buildSubjectDataIds,
  datasetDisplayName,
  displayDataIdText,
  recordRowsToTableRows,
  rowsToColumnNames,
  sortBrowseTableRows,
  timeSeriesRowsToTableRows,
  type BrowseDataId,
  type BrowseTableRow
} from "./browse-utils";
import { previewPagerText, usesPreviewPager, type ViewSortDirection } from "../view-browse/view-browse-utils";

defineOptions({ name: "DataBrowse" });

const props = withDefaults(
  defineProps<{
    pageTitle?: string;
    datasetOwnerModules?: OwnerModule[];
    datasetRoles?: DatasetRole[];
    includeUnowned?: boolean;
  }>(),
  {
    pageTitle: "数据浏览",
    datasetOwnerModules: undefined,
    datasetRoles: undefined,
    includeUnowned: false
  }
);

const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);

const activeDatasetId = ref("");
const datasets = ref<Dataset[]>([]);
const visibleDatasets = computed(() =>
  datasets.value.filter(item =>
    datasetMatchesAttribution(item, {
      ownerModules: props.datasetOwnerModules,
      datasetRoles: props.datasetRoles,
      includeUnowned: props.includeUnowned
    })
  )
);
const hasAttributionFilter = computed(() =>
  Boolean(props.datasetOwnerModules?.length || props.datasetRoles?.length || props.includeUnowned)
);
const datasetColumns = ref<DatasetColumn[]>([]);
const fields = ref<Field[]>([]);
const factors = ref<Factor[]>([]);
const dataIds = ref<BrowseDataId[]>([]);
const activeDataId = ref("");
const activeFreq = ref("");
const dataIdKeyword = ref("");
const tableRows = ref<BrowseTableRow[]>([]);
const tableColumnNames = ref<string[]>([]);
const detailRow = ref<BrowseTableRow>();
const detailVisible = ref(false);
const metaLoading = ref(false);
const contextLoading = ref(false);
const loading = ref(false);
const sortState = reactive<{ fieldName: string; direction: ViewSortDirection }>({ fieldName: "", direction: "" });
const timeSeriesPageResult = ref<PageResult>();
const recordPageResult = ref<PageResult>();
const timeSeriesPreviewHasMore = ref(false);
const recordPreviewHasMore = ref(false);

const DATA_BROWSE_PREVIEW_LIMIT = 1000;
const DEFAULT_DATA_PAGE_SIZE = 25;

const pagination = reactive({
  current: 1,
  pageSize: DEFAULT_DATA_PAGE_SIZE,
  total: 0
});

const currentDataset = computed(() => visibleDatasets.value.find(item => item.dataset_id === activeDatasetId.value));

const mode = computed<"none" | "time_series" | "record" | "missing">(() => {
  const dataset = currentDataset.value;
  if (!activeDatasetId.value) return "none";
  if (!dataset) return "missing";
  return isTimeSeriesDataKind(dataset.data_kind) ? "time_series" : "record";
});

const freqOptions = computed(() => currentDataset.value?.freqs || []);

const filteredDataIds = computed(() => {
  const keyword = dataIdKeyword.value.trim().toLowerCase();
  if (!keyword) return dataIds.value;
  return dataIds.value.filter(item => item.id.toLowerCase().includes(keyword) || item.name.toLowerCase().includes(keyword));
});

const tablePagination = computed(() => ({
  current: pagination.current,
  pageSize: pagination.pageSize,
  total: pagination.total,
  showTotal: true,
  showPageSize: true,
  showJumper: true,
  hideOnSinglePage: false,
  pageSizeOptions: [25, 50, 100, 200]
}));

const timeSeriesUsesPreviewPager = computed(() => usesPreviewPager(timeSeriesPageResult.value));
const recordUsesPreviewPager = computed(() => usesPreviewPager(recordPageResult.value));
const timeSeriesTablePagination = computed(() => (timeSeriesUsesPreviewPager.value ? false : tablePagination.value));
const recordTablePagination = computed(() => (recordUsesPreviewPager.value ? false : tablePagination.value));
const previewHasMore = computed(() => (mode.value === "record" ? recordPreviewHasMore.value : timeSeriesPreviewHasMore.value));

const preferredColumnNames = computed(() => datasetColumns.value.map(item => item.column_name).filter(Boolean));

const columnLabels = computed(() => buildColumnLabels(datasetColumns.value, fields.value, factors.value));

const detailColumns = computed(() => {
  const row = detailRow.value;
  if (!row) return [];
  const names = rowsToColumnNames([rowToSyntheticRecord(row)], tableColumnNames.value);
  return names.map(name => ({ name: columnTitle(name), value: row.values[name] || "-" }));
});

async function loadMeta() {
  const space_id = spaceStore.selectedSpaceId;
  if (!space_id) return;
  metaLoading.value = true;
  try {
    const page = { page: 1, size: 1000 };
    const [datasetItems, fieldRsp, factorRsp] = await Promise.all([
      hasAttributionFilter.value ? listAllDatasets(space_id) : listDatasets({ space_id, page }).then(rsp => rsp.datasets || []),
      listFields({ space_id, page }),
      listFactors({ space_id, page })
    ]);
    datasets.value = datasetItems;
    fields.value = fieldRsp.fields || [];
    factors.value = factorRsp.factors || [];
    ensureSelectedDataset();
    await loadDatasetContext();
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "加载数据集失败");
  } finally {
    metaLoading.value = false;
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

function ensureSelectedDataset() {
  if (!visibleDatasets.value.length) {
    activeDatasetId.value = "";
    return;
  }
  if (!activeDatasetId.value || !visibleDatasets.value.some(item => item.dataset_id === activeDatasetId.value)) {
    activeDatasetId.value = visibleDatasets.value[0].dataset_id;
  }
}

async function onDatasetChange() {
  clearBrowseState();
  await loadDatasetContext();
}

async function onFreqChange() {
  pagination.current = 1;
  await reloadRows();
}

function clearBrowseState() {
  datasetColumns.value = [];
  dataIds.value = [];
  activeDataId.value = "";
  activeFreq.value = "";
  dataIdKeyword.value = "";
  tableRows.value = [];
  tableColumnNames.value = [];
  pagination.current = 1;
  pagination.total = 0;
  timeSeriesPageResult.value = undefined;
  recordPageResult.value = undefined;
  timeSeriesPreviewHasMore.value = false;
  recordPreviewHasMore.value = false;
  sortState.fieldName = "";
  sortState.direction = "";
}

async function loadDatasetContext() {
  const space_id = spaceStore.selectedSpaceId;
  const dataset_id = activeDatasetId.value;
  if (!space_id || !dataset_id || mode.value === "none" || mode.value === "missing") return;

  contextLoading.value = true;
  try {
    await loadColumns(space_id, dataset_id);
    if (mode.value === "time_series") {
      await loadTimeSeriesDataIds(space_id, dataset_id);
      if (!activeFreq.value || !freqOptions.value.includes(activeFreq.value)) {
        activeFreq.value = freqOptions.value[0] || "";
      }
      if (dataIds.value.length > 0) {
        activeDataId.value = dataIds.value[0].id;
      }
    }
    await reloadRows();
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "加载浏览数据失败");
  } finally {
    contextLoading.value = false;
  }
}

async function loadColumns(space_id: string, dataset_id: string) {
  const rsp = await listDatasetColumns({ space_id, dataset_id, page: { page: 1, size: 1000 } });
  datasetColumns.value = rsp.columns || [];
}

async function loadTimeSeriesDataIds(space_id: string, dataset_id: string) {
  const [bindRsp, subjectRsp] = await Promise.all([
    listDatasetSubjects({ space_id, dataset_id, page: { page: 1, size: 1000 } }),
    listSubjects({ space_id, page: { page: 1, size: 1000 } })
  ]);
  dataIds.value = buildSubjectDataIds(bindRsp.dataset_subjects || [], subjectRsp.subjects || []);
}

async function selectDataId(dataId: string) {
  activeDataId.value = dataId;
  pagination.current = 1;
  await reloadRows();
}

async function reloadRows() {
  tableColumnNames.value = preferredColumnNames.value;
  if (mode.value === "time_series") {
    if (!activeDataId.value) {
      tableRows.value = [];
      pagination.total = 0;
      return;
    }
    await loadTimeSeriesRows();
    return;
  }
  if (mode.value === "record") {
    await loadRecordRows();
  }
}

async function loadTimeSeriesRows() {
  const space_id = spaceStore.requireSpaceId();
  const dataset_id = activeDatasetId.value;
  if (!dataset_id || !activeFreq.value) return;

  loading.value = true;
  try {
    const rsp = await readTimeSeriesRows({
      keys: [
        {
          space_id,
          dataset_id,
          subject_id: activeDataId.value,
          freq: activeFreq.value,
          dimensions: {}
        }
      ],
      order: accessSortOrder("data_time"),
      page: { page: pagination.current, size: pagination.pageSize }
    });
    const rows = rsp.rows || [];
    tableRows.value = sortBrowseTableRows(timeSeriesRowsToTableRows(rows), sortState.fieldName, sortState.direction);
    tableColumnNames.value = rowsToColumnNames(rows, preferredColumnNames.value);
    timeSeriesPageResult.value = rsp.page_result;
    timeSeriesPreviewHasMore.value = !!rsp.page_result?.has_more;
    pagination.total = timeSeriesUsesPreviewPager.value ? 0 : (rsp.page_result?.total ?? rows.length);
  } catch (error) {
    timeSeriesPageResult.value = undefined;
    timeSeriesPreviewHasMore.value = false;
    Message.error(error instanceof Error ? error.message : "加载时序数据失败");
  } finally {
    loading.value = false;
  }
}

async function loadRecordRows() {
  const space_id = spaceStore.requireSpaceId();
  const dataset_id = activeDatasetId.value;
  if (!dataset_id) return;

  loading.value = true;
  try {
    const rsp = await readRecordRows({
      keys: [{ space_id, dataset_id, record_id: "" }],
      order: accessSortOrder("version"),
      page: { page: pagination.current, size: pagination.pageSize }
    });
    const rows = rsp.rows || [];
    tableRows.value = sortBrowseTableRows(recordRowsToTableRows(rows), sortState.fieldName, sortState.direction);
    tableColumnNames.value = rowsToColumnNames(rows, preferredColumnNames.value);
    recordPageResult.value = rsp.page_result;
    recordPreviewHasMore.value = !!rsp.page_result?.has_more;
    pagination.total = recordUsesPreviewPager.value ? 0 : (rsp.page_result?.total ?? rows.length);
  } catch (error) {
    recordPageResult.value = undefined;
    recordPreviewHasMore.value = false;
    Message.error(error instanceof Error ? error.message : "加载记录数据失败");
  } finally {
    loading.value = false;
  }
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

function accessSortOrder(systemFieldName: "data_time" | "version"): SortOrder {
  if (sortState.fieldName === systemFieldName && sortState.direction) {
    return sortState.direction === "desc" ? "SORT_ORDER_DESC" : "SORT_ORDER_ASC";
  }
  return "SORT_ORDER_DESC";
}

function columnTitle(columnName: string) {
  return columnLabels.value[columnName] || columnName;
}

function dynamicColumnWidth(columnName: string) {
  return adaptiveColumnWidth(columnName, columnTitle(columnName), tableRows.value);
}

function openDetail(row: BrowseTableRow) {
  detailRow.value = row;
  detailVisible.value = true;
}

function rowToSyntheticRecord(row: BrowseTableRow): RecordRow {
  return {
    key: { space_id: "", dataset_id: "", record_id: row.key, version: row.version },
    columns: Object.keys(row.values).map(name => ({
      column_name: name,
      value_type: "FIELD_VALUE_TYPE_STRING",
      value: { string_value: row.values[name] }
    }))
  };
}

onMounted(loadMeta);
watch(selectedSpaceId, () => {
  activeDatasetId.value = "";
  clearBrowseState();
  loadMeta();
});
</script>

<style scoped>
.data-browse-page {
  width: 100%;
  height: 100%;
  min-width: 0;
}

.data-browse-page :deep(.arco-spin) {
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
  margin: 0 0 var(--moox-space-1);
  font-size: 20px;
  font-weight: 600;
}

.pane-head span,
.record-table-pane .table-toolbar span {
  color: var(--color-text-3);
}

.dataset-tabs-row {
  display: flex;
  align-items: flex-end;
  gap: 18px;
  min-width: 0;
  margin-bottom: var(--moox-space-3);
}

.dataset-tabs {
  min-width: 0;
  flex: 1;
}

.dataset-tabs :deep(.arco-tabs-content),
.freq-tabs :deep(.arco-tabs-content) {
  display: none;
}

.freq-tabs {
  flex: 0 0 auto;
}

.freq-tabs :deep(.arco-tabs-tab) {
  min-height: 26px;
  padding: var(--moox-space-1) 10px;
  border: 1px solid var(--color-border-2);
  border-radius: 4px;
}

.freq-tabs :deep(.arco-tabs-tab-active) {
  background: rgb(var(--primary-1));
  border-color: rgb(var(--primary-5));
}

.browse-shell {
  display: grid;
  grid-template-columns: minmax(200px, 240px) minmax(0, 1fr);
  align-items: start;
  gap: var(--moox-space-4);
  min-height: 500px;
  height: calc(100vh - 246px);
}

.data-id-pane,
.table-pane,
.record-table-pane {
  box-sizing: border-box;
  width: 100%;
  min-width: 0;
  border: 1px solid var(--color-border-2);
  border-radius: 8px;
  background: var(--color-bg-2);
}

.data-id-pane {
  display: flex;
  flex-direction: column;
  height: calc(100vh - 246px);
  min-height: 560px;
  max-height: 760px;
  overflow: hidden;
  padding: var(--moox-space-3);
}

.pane-head,
.table-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--moox-space-3);
}

.pane-head {
  margin-bottom: 10px;
}

.data-id-list {
  display: flex;
  flex: 1 1 auto;
  flex-direction: column;
  gap: var(--moox-space-1);
  height: 0;
  min-height: 0;
  margin-top: 10px;
  overflow: auto;
  overscroll-behavior: contain;
}

.data-id-list.loading {
  align-items: center;
  justify-content: center;
}

.data-id-item {
  flex: 0 0 auto;
  width: 100%;
  min-height: 36px;
  padding: var(--moox-space-2) 9px;
  overflow: hidden;
  color: var(--color-text-1);
  font-weight: 500;
  line-height: 20px;
  text-align: left;
  text-overflow: ellipsis;
  white-space: nowrap;
  cursor: pointer;
  background: transparent;
  border: 1px solid transparent;
  border-radius: 4px;
  transition:
    background-color 0.16s ease,
    border-color 0.16s ease;
}

.data-id-item:hover {
  background: var(--color-fill-2);
}

.data-id-item.active {
  background: rgb(var(--primary-1));
  border-color: rgb(var(--primary-5));
}

.table-pane,
.record-table-pane {
  padding: var(--moox-space-3);
}

.table-toolbar {
  margin-bottom: var(--moox-space-3);
}

.table-toolbar > div > strong,
.table-toolbar > div > span:not(.inline-freq) {
  display: block;
}

.table-toolbar > div > span:not(.inline-freq) {
  margin-top: 2px;
  color: var(--color-text-3);
  font-size: 12px;
}

.dataset-title-line {
  display: flex;
  align-items: baseline;
  gap: 6px;
  min-width: 0;
}

.dataset-title-line strong {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.table-toolbar .inline-freq {
  flex: 0 0 auto;
  color: var(--color-text-3);
  font-size: 12px;
}

.detail-body {
  display: flex;
  flex-direction: column;
  gap: var(--moox-space-4);
}

.detail-table {
  margin-top: var(--moox-space-1);
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

.preview-pager__page {
  min-width: 58px;
  color: var(--color-text-1);
  font-weight: 600;
  text-align: center;
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

@media (max-width: 960px) {
  .dataset-tabs-row {
    align-items: stretch;
    flex-direction: column;
  }

  .browse-shell {
    grid-template-columns: 1fr;
  }

  .data-id-pane {
    height: clamp(520px, 68vh, 720px);
    min-height: 520px;
  }
}
</style>
